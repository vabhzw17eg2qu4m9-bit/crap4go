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
// frames; one mutex guards stacks, stats bookkeeping and shard writes.
//
// Flush design (ported from upstream ad4f27b's observer-effect fix — the
// per-call log append was a file syscall inside every instrumented call):
//
//   - Each writer (one test binary/process × one instrumented package
//     instance) writes its OWN cumulative shard file
//     prof-<pid>-<shard>.json, replaced atomically (temp + rename) on
//     every flush. Shards never read other writers' data, so concurrent
//     writers cannot lose each other's updates (the host merges all
//     shards once after the test run: sum calls/totals, min minima,
//     max maxima).
//   - Nested method exits do NO I/O at all — only the outermost exit of
//     a goroutine's call tree can flush, and it ALWAYS flushes by
//     default: it is the only reliable delivery point (a process dying
//     after its last instrumented call has no other chance to report),
//     so data loss on normal termination is impossible. A flush is
//     O(#methods), never O(#calls).
//   - A backstop flush every CRAP_PROFILE_FLUSH_EVERY calls (default
//     100000) bounds the loss when a call stack is stuck non-empty (a
//     leaked frame whose exit never ran) or the process dies mid-tree.
//   - CRAP_PROFILE_FLUSH_MS (default 0) throttles outermost flushes to
//     at most one per interval — an OPT-IN trade: it keeps extremely
//     outermost-heavy loops I/O-light but the tail after the last
//     throttled exit is lost. Keep 0 unless that shape actually hurts.
//
// The '#flushes' diagnostic key in each shard (1-based flush count,
// advanced only on a successful rename) lets tests and debugging observe
// how many shard writes happened; the host merge skips '#'-prefixed keys.
//
// Divergence from upstream (documented honestly): Dart isolates are
// threads-in-one-process, so shard ids are boot-time identity hashes;
// Go test binaries are one process per package, so the pid already
// separates writers and the baked shard id only needs to separate
// package instances within one process. Upstream also keeps a public
// flush() for end-of-run calls — the Go instrumentation needs none
// (every enter is paired with a deferred exit, so the outermost exit is
// always reached).
func collectorSource(pkg string) string {
	collectorShardSeq++
	shardID := fmt.Sprintf("%s-%d", pkg, collectorShardSeq)
	return fmt.Sprintf(`package %[1]s

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// crap4goStatLine is one method's cumulative timing aggregate.
type crap4goStatLine struct {
	Calls           int64
	TotalMicros     int64
	TotalSelfMicros int64
	MinMicros       int64
	MaxMicros       int64
}

// crap4goFrame is one open instrumented call on a goroutine's stack.
type crap4goFrame struct {
	key         string
	start       time.Time
	childMicros int64
}

var (
	crap4goMu    sync.Mutex
	crap4goStats = map[string]*crap4goStatLine{}
	// crap4goStacks keys open frames by goroutine id; defers run on the
	// registering goroutine, so parallel test goroutines never cross.
	crap4goStacks     = map[int64][]*crap4goFrame{}
	crap4goCalls      int64
	crap4goFlushSeq   int64
	crap4goLastFlush  time.Time
	crap4goOutDir     = os.Getenv("CRAP_PROFILE_DIR")
	// crap4goShardID is baked per generated collector (package name +
	// generation sequence), so package instances sharing a pid keep
	// separate shard files.
	crap4goShardID            = "%[2]s"
	crap4goFlushMinIntervalMs = crap4goEnvInt("CRAP_PROFILE_FLUSH_MS", 0)
	crap4goFlushBackstop      = crap4goEnvInt("CRAP_PROFILE_FLUSH_EVERY", 100000)
)

// crap4goEnvInt reads a non-negative int64 env knob, falling back on
// absence or garbage.
func crap4goEnvInt(name string, fallback int64) int64 {
	v, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

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
// Nested exits only update memory; the flush decision is deferred to
// crap4goCheckpoint.
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
	crap4goRecord(frame.key, inclusive, self)
	crap4goCheckpoint(len(stack) == 0)
}

// crap4goRecord folds one completed call into the cumulative stats.
func crap4goRecord(key string, inclusive, self int64) {
	s := crap4goStats[key]
	if s == nil {
		s = &crap4goStatLine{MinMicros: inclusive}
		crap4goStats[key] = s
	}
	s.Calls++
	s.TotalMicros += inclusive
	s.TotalSelfMicros += self
	if inclusive < s.MinMicros {
		s.MinMicros = inclusive
	}
	if inclusive > s.MaxMicros {
		s.MaxMicros = inclusive
	}
	crap4goCalls++
}

// crap4goCheckpoint is the flush decision. The outermost exit of a call
// tree is the natural checkpoint: it always flushes by default (small
// unreported batch — keeps brief runs complete), then the opt-in
// interval throttles a hot outermost loop to at most one shard write
// per interval at the cost of a bounded tail. Nested exits flush only
// on the backstop, which bounds the tail when a stack is stuck
// non-empty (leaked frame) or the process dies between checkpoints.
func crap4goCheckpoint(outermost bool) {
	if outermost {
		if crap4goFlushMinIntervalMs <= 0 ||
			time.Since(crap4goLastFlush) >= time.Duration(crap4goFlushMinIntervalMs)*time.Millisecond {
			crap4goFlush()
		}
		return
	}
	if crap4goFlushBackstop > 0 && crap4goCalls%%crap4goFlushBackstop == 0 {
		crap4goFlush()
	}
}

// crap4goFlush writes this writer's cumulative stats to its own shard
// file, replaced atomically (temp + rename). The shard is private to
// this process and package instance — no read-merge of other writers'
// data, so concurrent writers cannot lose each other's updates by
// design; the host merges all shards after the test run. Write errors
// are ignored (best effort): the flush sequence only advances after a
// successful rename, so the next checkpoint retries the same number.
func crap4goFlush() {
	if crap4goOutDir == "" || len(crap4goStats) == 0 {
		return
	}
	data, err := crap4goSnapshotJSON()
	if err != nil {
		return
	}
	shardPath := filepath.Join(crap4goOutDir,
		fmt.Sprintf("prof-%%d-%%s.json", os.Getpid(), crap4goShardID))
	tmpPath := fmt.Sprintf("%%s.tmp.%%d", shardPath, crap4goFlushSeq)
	if os.WriteFile(tmpPath, data, 0o644) != nil {
		return
	}
	if os.Rename(tmpPath, shardPath) != nil {
		return
	}
	crap4goFlushSeq++
	crap4goLastFlush = time.Now()
}

// crap4goSnapshotJSON encodes the cumulative stats plus the '#flushes'
// diagnostic (a '#'-prefixed key the host merge skips) as one JSON map.
func crap4goSnapshotJSON() ([]byte, error) {
	snapshot := make(map[string]map[string]int64, len(crap4goStats)+1)
	for key, s := range crap4goStats {
		snapshot[key] = map[string]int64{
			"calls":           s.Calls,
			"totalMicros":     s.TotalMicros,
			"totalSelfMicros": s.TotalSelfMicros,
			"minMicros":       s.MinMicros,
			"maxMicros":       s.MaxMicros,
		}
	}
	snapshot["#flushes"] = map[string]int64{"calls": crap4goFlushSeq + 1}
	return json.Marshal(snapshot)
}
`, pkg, shardID)
}

// collectorShardSeq numbers generated collectors so writers sharing a
// package name (two directories can both declare `package util` in one
// test binary) keep separate shard files.
var collectorShardSeq int
