package main

import "fmt"

// collectorSource renders the collector file injected into every
// instrumented package of the temp module copy. It declares pkg's own package
// name so it compiles into that package; instrumented code calls
// crap4goEnter/crap4goExit from the same package, so no imports are added to
// instrumented files.
//
// Instrumented bodies call crap4goEnter on entry and defer crap4goExit, so
// the collector keeps a per-goroutine call stack of open frames and can
// record both the call's inclusive time and its self time (inclusive minus
// nested instrumented calls completed while the frame was open — flamegraph
// self-time semantics). The stack is keyed by goroutine id, and defers run
// on the registering goroutine, so parallel test goroutines never cross
// frames; one mutex guards stacks, stats bookkeeping and the log append.
//
// The collector appends one "<key>\t<inclusive>\t<self>" line per call to a
// per-process log file under CRAP_PROFILE_DIR (one file per test binary /
// pid, so no cross-process locking is needed). The duration is measured
// before any I/O, so logging overhead never pollutes the recorded time. The
// host merges the logs once after the test run.
func collectorSource(pkg string) string {
	return fmt.Sprintf(`package %[1]s

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// crap4goFrame is one open instrumented call on a goroutine's stack.
type crap4goFrame struct {
	key         string
	start       time.Time
	childMicros int64
}

var (
	crap4goMu     sync.Mutex
	crap4goFile   *os.File
	crap4goStacks = map[int64][]*crap4goFrame{}
)

// crap4goGoid returns the current goroutine's id, parsed from the header
// line runtime.Stack produces ("goroutine 123 [running]:").
//
// ponytail: this traceback is the µs-scale part of the per-call overhead;
// means under ~30µs are already flagged as profiler noise in reports. A
// faster goid would need unsafe runtime internals — not worth it while the
// ~-caveat covers the regime it pollutes.
func crap4goGoid() int64 {
	var buf [64]byte
	n := runtime.Stack(buf[:], false)
	fields := strings.Fields(string(buf[:n]))
	if len(fields) < 2 {
		return 0
	}
	id, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return id
}

// crap4goEnter marks instrumented method entry, pushing a frame on the
// calling goroutine's stack.
func crap4goEnter(key string) {
	id := crap4goGoid()
	start := time.Now()
	crap4goMu.Lock()
	defer crap4goMu.Unlock()
	crap4goStacks[id] = append(crap4goStacks[id], &crap4goFrame{key: key, start: start})
}

// crap4goExit marks instrumented method exit: inclusive is the frame's wall
// time (nested instrumented calls included); self is inclusive minus the
// nested calls that completed while the frame was open, clamped at zero.
func crap4goExit() {
	end := time.Now()
	id := crap4goGoid()
	crap4goMu.Lock()
	defer crap4goMu.Unlock()
	stack := crap4goStacks[id]
	if len(stack) == 0 {
		return
	}
	frame := stack[len(stack)-1]
	stack = stack[:len(stack)-1]
	inclusive := end.Sub(frame.start).Microseconds()
	self := inclusive - frame.childMicros
	if self < 0 {
		self = 0
	}
	if n := len(stack); n > 0 {
		stack[n-1].childMicros += inclusive
		crap4goStacks[id] = stack
	} else {
		delete(crap4goStacks, id)
	}
	crap4goLog(frame.key, inclusive, self)
}

// crap4goLog appends one recorded call to the per-process log file under
// CRAP_PROFILE_DIR, creating it lazily on first use.
func crap4goLog(key string, inclusive, self int64) {
	if crap4goFile == nil {
		f, err := os.Create(filepath.Join(os.Getenv("CRAP_PROFILE_DIR"), fmt.Sprintf("prof-%%d.jsonl", os.Getpid())))
		if err != nil {
			return
		}
		crap4goFile = f
	}
	fmt.Fprintf(crap4goFile, "%%s\t%%d\t%%d\n", key, inclusive, self)
}
`, pkg)
}
