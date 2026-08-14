package main

import (
	"fmt"
	"io"
	"path/filepath"
	"regexp"
	"strings"
)

// genericStems are lower-cased stems with no domain meaning. Files with these
// names accumulate unrelated declarations over time (ported from crap4dart's
// FileNamingGate).
var genericStems = map[string]bool{
	"common": true, "core": true, "general": true, "helper": true,
	"helpers": true, "misc": true, "shared": true, "stuff": true,
	"temp": true, "tmp": true, "types": true, "util": true,
	"utils": true, "utilities": true, "utility": true, "various": true,
}

// allowedNumericStems are whole stems accepted despite ending in digits —
// technical terms where the digits carry meaning. Mirrors crap4dart's
// FileNamingGateConfig.defaultAllowedStems verbatim.
var allowedNumericStems = map[string]bool{
	"aes128": true, "aes192": true, "aes256": true, "arm32": true,
	"arm64": true, "base32": true, "base64": true, "crc8": true,
	"crc16": true, "crc32": true, "f16": true, "f32": true,
	"f64": true, "h264": true, "h265": true, "http2": true,
	"http3": true, "i18n": true, "i2c": true, "int8": true,
	"int16": true, "int32": true, "int64": true, "ipv4": true,
	"ipv6": true, "l10n": true, "a11y": true, "md5": true,
	"oauth1": true, "oauth2": true, "sha1": true, "sha256": true,
	"sha384": true, "sha512": true, "uint8": true, "uint16": true,
	"uint32": true, "uint64": true, "utf8": true, "utf16": true,
	"utf32": true, "w3c": true, "webgl2": true, "x509": true,
	"x86": true, "x64": true,
}

// numericSuffixRe matches a stem ending in digits preceded by a letter or
// underscore, as in "jira_batch1", "report2", "day_1" or "configv3".
var numericSuffixRe = regexp.MustCompile(`[a-z_][0-9]+$`)

// FileViolation is one mechanically-named file: its path relative to the
// project root plus the violation message.
type FileViolation struct {
	Path    string
	Message string
}

// violationForStem returns the violation message for a Go file stem (name
// without the ".go" extension), or "" when the name is acceptable. Generic
// stems always violate; otherwise a numeric suffix violates unless the whole
// stem is allowlisted.
func violationForStem(stem string) string {
	lower := strings.ToLower(stem)
	if genericStems[lower] {
		return fmt.Sprintf("generic name %q — split by domain instead of accumulating unrelated declarations", stem+".go")
	}
	if numericSuffixRe.MatchString(lower) && !allowedNumericStems[lower] {
		return fmt.Sprintf("numeric suffix in %q — split by domain instead of numbered parts (batch1, part2, v2 ...)", stem+".go")
	}
	return ""
}

// CheckFileNaming evaluates every selected file against the naming rules and
// returns the violations plus the number of files checked.
func CheckFileNaming(files []string, root string) ([]FileViolation, int) {
	var violations []FileViolation
	for _, f := range files {
		stem := strings.TrimSuffix(filepath.Base(f), ".go")
		if msg := violationForStem(stem); msg != "" {
			violations = append(violations, FileViolation{
				Path:    relPath(root, f),
				Message: msg,
			})
		}
	}
	return violations, len(files)
}

// RunFileNamingCommand implements `crap4go file-naming [paths...]`: it checks
// the selected Go files for mechanical names (generic stems or numeric
// suffixes), prints one line per violation plus a summary, and returns exit
// code 2 iff there are violations. Selection defaults to the normal analyze
// rules (all non-test, non-vendor .go files under root).
func RunFileNamingCommand(args []string, root string, stdout, stderr io.Writer) int {
	files, err := selectFiles(false, args, root)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if len(files) == 0 {
		fmt.Fprintln(stdout, "No Go files to check.")
		return 0
	}
	violations, checked := CheckFileNaming(files, root)
	for _, v := range violations {
		fmt.Fprintf(stdout, "%s: %s\n", v.Path, v.Message)
	}
	if len(violations) > 0 {
		fmt.Fprintf(stdout, "%d/%d files with mechanical names\n", len(violations), checked)
		return 2
	}
	fmt.Fprintf(stdout, "%d files have domain-meaningful names\n", checked)
	return 0
}
