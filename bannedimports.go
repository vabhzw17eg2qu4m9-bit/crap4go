package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
	"io"
	"regexp"
	"strings"
	"sync"
)

// ImportRule is one architectural boundary from crap4dart's
// BannedImportsGate: every file matching From must not import any path
// matching Forbid (raw import path or resolved project-relative path).
type ImportRule struct {
	From    string
	Forbid  string
	Message string
}

// BannedImportViolation is one banned import in a file matching a rule's
// from glob.
type BannedImportViolation struct {
	Path    string
	Line    int
	Message string
}

// RunBannedImportsCommand implements `crap4go banned-imports
// [--from GLOB --forbid GLOB --message MSG]... [paths...]`: rules are
// from/forbid pairs zipped by CLI order; --message is optional per rule.
// With no rules it prints a pass and exits 0.
func RunBannedImportsCommand(args []string, root string, stdout, stderr io.Writer) int {
	rules, paths, code := parseImportRuleFlags(args, stderr)
	if code != 0 {
		return code
	}
	if len(rules) == 0 {
		fmt.Fprintln(stdout, "no rules configured")
		return 0
	}
	files, err := selectFiles(false, paths, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckBannedImports(files, root, rules)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s:%d: %s\n", v.Path, v.Line, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d banned import(s) in %d files\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d files comply with %d rule(s)\n", checked, len(rules))
	return 0
}

// parseImportRuleFlags parses the --from/--forbid/--message flags plus the
// positional paths; the returned code is 1 on a usage error (already
// reported), 0 otherwise.
func parseImportRuleFlags(args []string, stderr io.Writer) ([]ImportRule, []string, int) {
	fs := flag.NewFlagSet("crap4go banned-imports", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := &stringSlice{}
	forbid := &stringSlice{}
	message := &stringSlice{}
	fs.Var(from, "from", "glob of files the rule applies to (repeatable)")
	fs.Var(forbid, "forbid", "glob of banned import paths (repeatable)")
	fs.Var(message, "message", "explanation appended to violations (optional, repeatable)")
	if err := fs.Parse(args); err != nil {
		return nil, nil, 1
	}
	rules, err := zipRules(from.slice(), forbid.slice(), message.slice())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return nil, nil, 1
	}
	return rules, fs.Args(), 0
}

// zipRules zips the --from/--forbid/--message flag values by CLI order into
// rules. From and forbid must appear in pairs; messages are optional and
// padded with empty strings.
func zipRules(from, forbid, message []string) ([]ImportRule, error) {
	if len(forbid) != len(from) {
		return nil, fmt.Errorf("--from and --forbid must appear in pairs (got %d and %d)", len(from), len(forbid))
	}
	if len(message) > len(from) {
		return nil, fmt.Errorf("--message given more often than --from/--forbid (%d > %d)", len(message), len(from))
	}
	rules := make([]ImportRule, len(from))
	for i := range from {
		rules[i] = ImportRule{From: from[i], Forbid: forbid[i]}
		if i < len(message) {
			rules[i].Message = message[i]
		}
	}
	return rules, nil
}

// stringSlice is a repeatable string flag value.
type stringSlice struct {
	values []string
}

func (s *stringSlice) String() string { return strings.Join(s.values, ",") }

func (s *stringSlice) Set(v string) error {
	s.values = append(s.values, v)
	return nil
}

func (s *stringSlice) slice() []string { return s.values }

// CheckBannedImports applies the rules to every selected file matching any
// from glob and returns the violations plus the number of files checked.
func CheckBannedImports(files []string, root string, rules []ImportRule) ([]BannedImportViolation, int) {
	module := modulePath(root)
	var violations []BannedImportViolation
	checked := 0
	for _, f := range files {
		rel := relPath(root, f)
		applicable := applicableRules(rules, rel)
		if len(applicable) == 0 {
			continue
		}
		checked++
		file, fset, err := parseGoFile(f)
		if err != nil {
			continue
		}
		for _, spec := range file.Imports {
			violations = append(violations, specViolations(spec, fset, rel, applicable, module)...)
		}
	}
	return violations, checked
}

// applicableRules returns the rules whose from glob matches the
// project-relative path.
func applicableRules(rules []ImportRule, rel string) []ImportRule {
	var out []ImportRule
	for _, r := range rules {
		if globMatch(r.From, rel) {
			out = append(out, r)
		}
	}
	return out
}

// specViolations returns one violation per rule banning this import spec.
func specViolations(spec *ast.ImportSpec, fset *token.FileSet, rel string, rules []ImportRule, module string) []BannedImportViolation {
	var out []BannedImportViolation
	path := unquoted(spec)
	for _, r := range rules {
		if !importMatches(r.Forbid, path, module) {
			continue
		}
		msg := fmt.Sprintf("import %q is banned for %s", path, rel)
		if r.Message != "" {
			msg += " — " + r.Message
		}
		out = append(out, BannedImportViolation{
			Path:    rel,
			Line:    fset.Position(spec.Pos()).Line,
			Message: msg,
		})
	}
	return out
}

// importMatches reports whether the forbid glob matches the raw import path
// or its resolved project-relative directory (for imports inside the
// module).
func importMatches(forbid, path, module string) bool {
	if globMatch(forbid, path) {
		return true
	}
	resolved := resolveImportDir(path, module)
	return resolved != "" && globMatch(forbid, resolved)
}

// globRegexes caches compiled glob patterns once per pattern instead of once
// per matched file (ported from crap4dart 0.8.6).
var globRegexes sync.Map // pattern -> *regexp.Regexp (nil when invalid)

// globMatch reports whether name matches a glob pattern supporting '*', '?'
// and '**' (matching across path separators).
func globMatch(pattern, name string) bool {
	if v, ok := globRegexes.Load(pattern); ok {
		re := v.(*regexp.Regexp)
		return re != nil && re.MatchString(name)
	}
	re, err := regexp.Compile(globToRegex(pattern))
	if err != nil {
		re = nil
	}
	globRegexes.Store(pattern, re)
	return re != nil && re.MatchString(name)
}

// globToRegex translates a glob pattern into an anchored regular
// expression. A leading "**/" and a trailing "/**" may also match zero
// directories, so "**/db/**" matches "db" itself.
func globToRegex(pattern string) string {
	return `^` + globCore(pattern) + `$`
}

// globCore translates a glob pattern into an unanchored expression segment.
func globCore(pattern string) string {
	if rest, ok := strings.CutSuffix(pattern, "/**"); ok {
		return globCore(rest) + `(?:/.*)?`
	}
	if rest, ok := strings.CutPrefix(pattern, "**/"); ok {
		return `(?:.*/)?` + globCore(rest)
	}
	return globChars(pattern)
}

// globChars translates the plain (non-`**`-anchored) part of a glob: `*`
// matches within a segment, `**` across segments, `?` one character.
func globChars(pattern string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(`.*`)
				i++
			} else {
				b.WriteString(`[^/]*`)
			}
		case '?':
			b.WriteString(`[^/]`)
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return b.String()
}
