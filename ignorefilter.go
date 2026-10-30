package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Ignore comments suppress ignorable gates' violations (ported from
// crap4dart 8abb64c's ignore_filter.dart). The legacy blanket markers
// keep their scope; the granular Dart-lint-style markers carry ONE gate
// id each (the Go subcommand name), several markers on one line union
// their ids, and an empty id (`// ignore:`) is blanket again. Comma
// lists are not lists — a comma id is one unknown id and matches
// nothing, like any unknown id.
const (
	// ignoreMarker is the legacy line-scoped blanket marker: suppresses
	// every ignorable gate's violation on its line (or the line above).
	ignoreMarker = "crap:ignore"
	// ignoreFileMarker is the legacy file-scoped blanket marker (anywhere
	// in the first 5 lines): suppresses all ignorable gates' violations
	// in the whole file.
	ignoreFileMarker = "crap:ignore-file"
)

// ignoreFileScopeLines is how many leading lines carry file-scoped markers.
const ignoreFileScopeLines = 5

// ignoreLineRule matches `// ignore:<id>` — one id per marker, ending at
// whitespace or the next comment start (`/`).
var ignoreLineRule = regexp.MustCompile(`//\s*ignore:([^\s/]*)`)

// ignoreFileRule matches `// ignore-file:<id>` with the same grammar.
var ignoreFileRule = regexp.MustCompile(`//\s*ignore-file:([^\s/]*)`)

// ignorableGates is the per-gate opt-in — crap4go's hard-coded host
// adaptation of upstream's `ignorable: true` config flag (this port has
// no config file): only these gates' violations are suppressible, by
// blanket and granular markers alike. All gates whose violations carry
// a file path are ignorable; line-less violations (file-naming,
// unused-files) respond to file-scoped markers only. Not ignorable:
// folder-structure violates directories (no lines to read),
// similarity reports clusters whose peers still name a suppressed file,
// mutation prints count-bearing aggregates that filtering would desync,
// and memory/profile are informational, never failing.
var ignorableGates = map[string]bool{
	"anon-struct-signatures": true,
	"banned-imports":         true,
	"class-size":             true,
	"collapsible-flow":       true,
	"complexity":             true,
	"duplicates":             true,
	"file-naming":            true,
	"identical-branches":     true,
	"magic-constants":        true,
	"nesting":                true,
	"scattered-constants":    true,
	"string-hazards":         true,
	"test-assertions":        true,
	"unused-code":            true,
	"unused-files":           true,
	"weight-of-class":        true,
}

// ignoreAware is the location view the shared filter needs from every
// gate's violation type; violations without a line report 0.
type ignoreAware interface {
	ignorePath() string
	ignoreLine() int
}

// ignoreScope is the gate-id set carried by one ignore comment: blanket
// marks the no-id form (suppress everything ignorable), ids the named
// gates.
type ignoreScope struct {
	blanket bool
	ids     map[string]bool
}

// ignoreChecker resolves marker lookups for one gate run, caching each
// file's lines so a violation-heavy gate reads it once. Unreadable and
// empty files cache nil and are never suppressed.
type ignoreChecker struct {
	root  string
	lines map[string][]string
}

func (s ignoreScope) matches(gateID string) bool {
	return s.blanket || s.ids[gateID]
}

func (s ignoreScope) union(other ignoreScope) ignoreScope {
	merged := make(map[string]bool, len(s.ids)+len(other.ids))
	for id := range s.ids {
		merged[id] = true
	}
	for id := range other.ids {
		merged[id] = true
	}
	return ignoreScope{blanket: s.blanket || other.blanket, ids: merged}
}

// parseIgnoreLine parses every `ignore:`-style marker on one source line
// and unions their gate ids — one gate per marker, Dart lint style. An
// empty id makes the scope blanket.
func parseIgnoreLine(line string, rule *regexp.Regexp) ignoreScope {
	var scope ignoreScope
	for _, m := range rule.FindAllStringSubmatch(line, -1) {
		if id := strings.TrimSpace(m[1]); id != "" {
			if scope.ids == nil {
				scope.ids = map[string]bool{id: true}
				continue
			}
			scope.ids[id] = true
			continue
		}
		scope.blanket = true
	}
	return scope
}

// filterIgnored drops violations of gateID suppressed by ignore markers.
// A violation is suppressed when its file carries a file-scoped marker
// naming the gate — `// crap:ignore-file` (everything ignorable) or
// `// ignore-file:<id>` — within the first 5 lines, or when the
// violation's line or the stacked whole-line marker run above it carries
// a line-scoped one — `// crap:ignore` or `// ignore:<id>`. Violations
// without a line are only file-suppressible. No-op unless gateID opted
// in via ignorableGates.
func filterIgnored[T ignoreAware](gateID, root string, violations []T) []T {
	if !ignorableGates[gateID] || len(violations) == 0 {
		return violations
	}
	c := &ignoreChecker{root: root, lines: map[string][]string{}}
	kept := violations[:0]
	for _, v := range violations {
		if !c.suppressed(gateID, v.ignorePath(), v.ignoreLine()) {
			kept = append(kept, v)
		}
	}
	return kept
}

// line returns the 1-indexed source line, or ok=false past EOF or for an
// unreadable file.
func (c *ignoreChecker) line(path string, n int) (string, bool) {
	lines, ok := c.lines[path]
	if !ok {
		if data, err := os.ReadFile(path); err == nil {
			lines = strings.Split(string(data), "\n")
		}
		c.lines[path] = lines
	}
	if n < 1 || n > len(lines) {
		return "", false
	}
	return lines[n-1], true
}

// suppressed reports whether one gateID violation at file:line is
// suppressed. Relative violation paths resolve against the project root.
func (c *ignoreChecker) suppressed(gateID, file string, line int) bool {
	path := file
	if !filepath.IsAbs(path) {
		path = filepath.Join(c.root, file)
	}
	if c.fileSuppressed(path, gateID) {
		return true
	}
	if line == 0 {
		return false
	}
	return c.lineSuppressed(path, line, gateID)
}

// fileSuppressed reports whether path's first five lines carry a
// file-scoped marker suppressing gateID (unreadable files never match).
func (c *ignoreChecker) fileSuppressed(path, gateID string) bool {
	var scope ignoreScope
	for n := 1; n <= ignoreFileScopeLines; n++ {
		text, ok := c.line(path, n)
		if !ok {
			break
		}
		if strings.Contains(text, ignoreFileMarker) {
			scope.blanket = true
		}
		scope = scope.union(parseIgnoreLine(text, ignoreFileRule))
	}
	return scope.matches(gateID)
}

// lineSuppressed reports whether the violation's own line carries a
// suppressing marker, or the run of whole-line ignore comments directly
// above it names gateID — stacked `// ignore: a` / `// ignore: b` lines
// all apply to the code line that ends the run.
func (c *ignoreChecker) lineSuppressed(path string, line int, gateID string) bool {
	if text, ok := c.line(path, line); ok && lineSuppresses(text, gateID) {
		return true
	}
	for n := line - 1; n >= 1; n-- {
		text, ok := c.line(path, n)
		if ok && lineSuppresses(text, gateID) {
			return true
		}
		if !ok || !wholeLineIgnoreMarker(text) {
			return false
		}
	}
	return false
}

// lineSuppresses reports whether one source line carries any marker
// suppressing gateID — the legacy blanket marker or a granular one
// naming the gate.
func lineSuppresses(text, gateID string) bool {
	return strings.Contains(text, ignoreMarker) ||
		parseIgnoreLine(text, ignoreLineRule).matches(gateID)
}

// wholeLineIgnoreMarker reports whether the line is nothing but an ignore
// comment — only such lines continue the stacked-marker run above a
// violation.
func wholeLineIgnoreMarker(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "// ignore:") ||
		strings.HasPrefix(trimmed, "// ignore-file:")
}
