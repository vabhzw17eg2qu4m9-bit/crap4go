package main

import "fmt"

// collectorSource renders the collector file injected into every
// instrumented package of the temp module copy. It declares pkg's own package
// name so it compiles into that package; instrumented code calls
// crap4goRecord from the same package, so no imports are added to
// instrumented files.
//
// The collector appends one "<key>\t<micros>" line per call to a per-process
// log file under CRAP_PROFILE_DIR (one file per test binary / pid, so no
// cross-process locking is needed). The duration is measured before any I/O,
// so logging overhead never pollutes the recorded time. The host merges the
// logs after the test run.
func collectorSource(pkg string) string {
	return fmt.Sprintf(`package %[1]s

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	crap4goMu   sync.Mutex
	crap4goFile *os.File
)

// crap4goRecord starts a timer for key; defer the returned closure to log the
// elapsed time on exit.
func crap4goRecord(key string) func() {
	start := time.Now()
	return func() { crap4goLog(key, time.Since(start).Microseconds()) }
}

func crap4goLog(key string, micros int64) {
	crap4goMu.Lock()
	defer crap4goMu.Unlock()
	if crap4goFile == nil {
		f, err := os.Create(filepath.Join(os.Getenv("CRAP_PROFILE_DIR"), fmt.Sprintf("prof-%%d.jsonl", os.Getpid())))
		if err != nil {
			return
		}
		crap4goFile = f
	}
	fmt.Fprintf(crap4goFile, "%%s\t%%d\n", key, micros)
}
`, pkg)
}
