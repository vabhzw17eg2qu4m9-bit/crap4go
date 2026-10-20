package main

import (
	"go/scanner"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Similarity engine, ported from crap4dart 2b21966 (§11.23): file-level
// near-duplicate detection over any text corpus — copied docs that
// drifted apart, twin templates, stale snapshot copies, generated files
// duplicating their sources. Files become shingle sets (8-token windows
// of profile-normalized text), pairs cross when Jaccard or containment
// reaches the threshold, and transitive pairs merge into clusters via
// union-find.

// Similarity gate defaults (ported from crap4dart's SimilarityGateConfig).
const (
	// shingleSize is the number of consecutive tokens forming one shingle.
	shingleSize = 8
	// defaultSimMinShingles skips tiny files — they carry no reliable signal.
	defaultSimMinShingles = 50
	defaultSimJaccard     = 0.8
	defaultSimContainment = 0.95
)

var (
	defaultSimInclude = []string{"**/*.md"}
	// defaultSimExclude is the upstream list with .dart_tool replaced by
	// vendor/** (the Go port's standard exclusion, as in duplicates).
	defaultSimExclude = []string{".git/**", "build/**", "coverage/**", "node_modules/**", "vendor/**"}
)

// similarityProfile is a content profile: how a file's raw bytes become
// comparable text. Profiles strip the noise that makes two unrelated
// files look alike (imports, comments, markdown fences) so the remaining
// signal is actual content.
type similarityProfile int

const (
	// profileMarkdown: prose documents — fenced code blocks are dropped,
	// only the prose words are compared.
	profileMarkdown similarityProfile = iota
	// profileCode: source code — comments and import-family directives
	// are stripped, literals become placeholders, identifiers lowercased.
	profileCode
	// profileRaw: anything else — compared as-is.
	profileRaw
)

var markdownExtensions = map[string]bool{".md": true, ".markdown": true, ".mdx": true}

// codeExtensions covers any language — the generic normalizer is
// language-agnostic token shaping; .go files take the go/scanner stream
// (similarityGoTokens) instead.
var codeExtensions = map[string]bool{
	".dart": true, ".js": true, ".mjs": true, ".cjs": true, ".ts": true,
	".tsx": true, ".jsx": true, ".java": true, ".py": true, ".kt": true,
	".kts": true, ".swift": true, ".go": true, ".rs": true, ".c": true,
	".h": true, ".cpp": true, ".hpp": true, ".cs": true, ".rb": true,
	".php": true, ".scala": true, ".gradle": true, ".groovy": true,
	".sh": true, ".sql": true,
}

// similarityProfileFor is the profile for a file path (by extension),
// profileRaw for unknown extensions.
func similarityProfileFor(path string) similarityProfile {
	ext := strings.ToLower(filepath.Ext(path))
	if markdownExtensions[ext] {
		return profileMarkdown
	}
	if codeExtensions[ext] {
		return profileCode
	}
	return profileRaw
}

// Markdown prose: drops fenced code blocks, keeps heading/list words.
func normalizeMarkdown(content string) string {
	var b strings.Builder
	inFence := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || trimmed == "" {
			continue
		}
		b.WriteString(trimmed)
		b.WriteByte(' ')
	}
	return b.String()
}

// Line-comment markers across the code extension family.
var simLineComment = regexp.MustCompile(`^\s*(//|#|--|;)`)

// Import-family directives across languages — boilerplate that would
// otherwise make every file of a project look like every other.
var simImportLines = []*regexp.Regexp{
	regexp.MustCompile(`^\s*(import|export|part|part of|library)\b`),
	regexp.MustCompile(`^\s*(from|require|require_once|using|package|include)\b`),
	regexp.MustCompile(`^#!`),
}

// Source code (generic, non-Go): strips block/line comments and
// import-family lines; literals are placeholdered and identifiers
// lowercased at token time.
func normalizeCode(content string) string {
	var kept []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		stripped, stillOpen := simStripBlockComments(line, inBlock)
		inBlock = stillOpen
		if simIsCodeNoise(stripped) {
			continue
		}
		kept = append(kept, strings.TrimSpace(stripped))
	}
	return strings.Join(kept, "\n")
}

// simStripBlockComments removes block comments from line; stillInBlock
// carries an unterminated /* across lines.
func simStripBlockComments(line string, inBlock bool) (string, bool) {
	if inBlock {
		close := strings.Index(line, "*/")
		if close < 0 {
			return "", true
		}
		return simStripBlockComments(line[close+2:], false)
	}
	open := strings.Index(line, "/*")
	if open < 0 {
		return line, false
	}
	close := strings.Index(line[open+2:], "*/")
	if close < 0 {
		return line[:open], true
	}
	joined := line[:open] + line[open+close+2:]
	return simStripBlockComments(joined, false)
}

// simIsCodeNoise reports whether a stripped code line carries no
// comparison signal (empty, a line comment, or an import directive).
func simIsCodeNoise(line string) bool {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || simLineComment.MatchString(trimmed) {
		return true
	}
	for _, pattern := range simImportLines {
		if pattern.MatchString(trimmed) {
			return true
		}
	}
	return false
}

// similarityNormalize is the normalizer for a profile; markdown and code
// are shaped, raw content passes through.
func similarityNormalize(content string, profile similarityProfile) string {
	switch profile {
	case profileMarkdown:
		return normalizeMarkdown(content)
	case profileCode:
		return normalizeCode(content)
	default:
		return content
	}
}

// Word tokens for prose and raw content: letters/digits runs of 2+
// characters (latin + cyrillic), lowercased.
var simWordToken = regexp.MustCompile(`[0-9A-Za-zА-Яа-яЁё]{2,}`)

// Code tokens: identifiers, numbers and quoted strings.
var simCodeToken = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*|'[^']*'|"[^"]*"|\d+(\.\d+)?`)

// simNumberStart marks number tokens (mapped to a placeholder for code).
var simNumberStart = regexp.MustCompile(`^\d`)

// tokensFor splits content into comparison tokens according to profile.
//
// Markdown/raw: prose words. Code: identifiers lowercased, numbers and
// strings replaced with N/S placeholders so clones differing only in
// literal values still match.
func tokensFor(content string, profile similarityProfile) []string {
	switch profile {
	case profileMarkdown, profileRaw:
		matches := simWordToken.FindAllString(content, -1)
		out := make([]string, len(matches))
		for i, m := range matches {
			out[i] = strings.ToLower(m)
		}
		return out
	default:
		matches := simCodeToken.FindAllString(content, -1)
		out := make([]string, len(matches))
		for i, m := range matches {
			out[i] = simCodeTokenValue(m)
		}
		return out
	}
}

// simCodeTokenValue is the comparison value of one raw code token.
func simCodeTokenValue(tok string) string {
	if strings.HasPrefix(tok, "'") || strings.HasPrefix(tok, `"`) {
		return "S"
	}
	if simNumberStart.MatchString(tok) {
		return "N"
	}
	return strings.ToLower(tok)
}

// similarityGoTokens tokenizes Go source with the go/scanner stream (the
// port convention, as in duplicates): identifiers and keywords
// lowercased, string/char literals → S, numbers → N, comments and
// operators dropped — and whole import declarations dropped (the Go
// import family), the boilerplate that would otherwise make every file
// of a project look like every other. Scan errors are ignored: a partly
// valid file still contributes its valid tokens.
func similarityGoTokens(src string) []string {
	fset := token.NewFileSet()
	file := fset.AddFile("", fset.Base(), len(src))
	var s scanner.Scanner
	s.Init(file, []byte(src), nil /* scan errors ignored */, 0)
	var tokens []string
	inImport, importDepth := false, 0
	for {
		_, tok, lit := s.Scan()
		if tok == token.EOF {
			return tokens
		}
		if inImport {
			inImport = similaritySkipImport(tok, &importDepth)
			continue
		}
		if tok == token.IMPORT {
			inImport, importDepth = true, 0
			continue
		}
		if value, keep := similarityGoTokenValue(tok, lit); keep {
			tokens = append(tokens, value)
		}
	}
}

// similaritySkipImport consumes one token inside an import declaration
// and reports whether the declaration is still open: the whole decl
// (keyword, parens, every spec) drops out.
func similaritySkipImport(tok token.Token, depth *int) bool {
	switch tok {
	case token.LPAREN:
		*depth++
	case token.RPAREN:
		*depth--
		return *depth > 0
	case token.SEMICOLON:
		return *depth > 0
	}
	return true
}

// similarityGoTokenValue is the comparison value of one scanned token:
// identifiers and keywords lowercased, literals placeholdered. keep is
// false for tokens that carry no shingle signal (operators,
// punctuation, semicolons, illegal runes).
func similarityGoTokenValue(tok token.Token, lit string) (value string, keep bool) {
	if tok == token.IDENT || tok.IsKeyword() {
		return strings.ToLower(lit), true
	}
	return similarityGoLiteralValue(tok)
}

// similarityGoLiteralValue maps literal tokens to their comparison
// placeholders: strings and runes → S, numbers → N.
func similarityGoLiteralValue(tok token.Token) (value string, keep bool) {
	switch tok {
	case token.STRING, token.CHAR:
		return "S", true
	case token.INT, token.FLOAT, token.IMAG:
		return "N", true
	}
	return "", false
}

// shinglesForPath is the set of shingleSize-token shingles of content
// under the profile for path, or an empty set when there are fewer
// tokens than one shingle (such files carry no similarity signal).
//
// The content is normalized first (markdown loses its fenced code
// blocks, code loses comments/imports and keeps literal placeholders) —
// raw bytes would make unrelated files look alike.
func shinglesForPath(content, path string) map[string]struct{} {
	profile := similarityProfileFor(path)
	var tokens []string
	if profile == profileCode && strings.EqualFold(filepath.Ext(path), ".go") {
		tokens = similarityGoTokens(content)
	} else {
		tokens = tokensFor(similarityNormalize(content, profile), profile)
	}
	set := make(map[string]struct{})
	if len(tokens) < shingleSize {
		return set
	}
	for i := 0; i+shingleSize <= len(tokens); i++ {
		set[strings.Join(tokens[i:i+shingleSize], "\x1f")] = struct{}{}
	}
	return set
}

// intersectionCount is the number of shingles present in both sets.
func intersectionCount(a, b map[string]struct{}) int {
	small, large := a, b
	if len(b) < len(a) {
		small, large = b, a
	}
	shared := 0
	for shingle := range small {
		if _, ok := large[shingle]; ok {
			shared++
		}
	}
	return shared
}

// jaccard is the Jaccard similarity of two shingle sets: intersection
// over union. 1.0 for identical sets, 0.0 for disjoint ones.
func jaccard(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := intersectionCount(a, b)
	return float64(shared) / float64(len(a)+len(b)-shared)
}

// containment is the containment of the smaller set in the larger one:
// how much of the smaller file's content the larger one reproduces.
// Catches the "file A is largely a copy of big file B" case where
// Jaccard stays low.
func containment(a, b map[string]struct{}) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	shared := intersectionCount(a, b)
	return float64(shared) / float64(min(len(a), len(b)))
}

// similarityFile is one analyzed file: its project-relative path and
// shingle set.
type similarityFile struct {
	path     string
	shingles map[string]struct{}
}

// SimilarityPair is a pair of files whose similarity crosses at least
// one threshold.
type SimilarityPair struct {
	A           string
	B           string
	Jaccard     float64
	Containment float64
}

// SimilarityCluster is a connected group of similar files (transitively
// merged pairs).
type SimilarityCluster struct {
	Files []string
	Pairs []SimilarityPair
}

// SimilarityReport is the outcome of a similarity scan.
type SimilarityReport struct {
	// Scanned is the number of files read and analyzed.
	Scanned int
	// Skipped counts files skipped for carrying fewer shingles than the
	// floor.
	Skipped int
	// Pairs are the threshold-crossing pairs, sorted by Jaccard
	// descending.
	Pairs []SimilarityPair
	// Clusters are the groups of ≥ 2 files, largest first.
	Clusters []SimilarityCluster
}

// Clean reports whether nothing crossed the thresholds.
func (r SimilarityReport) Clean() bool { return len(r.Pairs) == 0 }

// ClusteredPaths lists the paths inside all clusters.
func (r SimilarityReport) ClusteredPaths() []string {
	var out []string
	for _, c := range r.Clusters {
		out = append(out, c.Files...)
	}
	return out
}

// MetricsFor returns the strongest pair metrics path participates in.
// Pairs are sorted strongest-first, so the first hit wins.
func (r SimilarityReport) MetricsFor(path string) (jaccard, containment float64, ok bool) {
	for _, pair := range r.Pairs {
		if pair.A == path || pair.B == path {
			return pair.Jaccard, pair.Containment, true
		}
	}
	return 0, 0, false
}

// similarityOptions holds the scan configuration (flag values of the
// similarity subcommand; upstream gate config keys dirs/include/exclude/
// min_shingles/jaccard/containment).
type similarityOptions struct {
	dirs        []string
	include     []string
	exclude     []string
	jaccard     float64
	containment float64
	minShingles int
}

// analyzeSimilarity scans dirs (project-relative, recursive) under root,
// keeping files matching include and dropping exclude globs. Files with
// fewer than minShingles shingles are skipped — tiny files carry no
// reliable signal. Pairs cross when Jaccard ≥ jaccard OR containment ≥
// containment.
func analyzeSimilarity(root string, opts similarityOptions) SimilarityReport {
	var analyzed []similarityFile
	skipped := 0
	for _, path := range collectSimilarityFiles(root, opts.dirs, opts.include, opts.exclude) {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			skipped++
			continue
		}
		shingles := shinglesForPath(string(data), path)
		if len(shingles) < opts.minShingles {
			skipped++
			continue
		}
		analyzed = append(analyzed, similarityFile{path: path, shingles: shingles})
	}
	pairs := similarityPairs(analyzed, opts.jaccard, opts.containment)
	return SimilarityReport{
		Scanned:  len(analyzed),
		Skipped:  skipped,
		Pairs:    pairs,
		Clusters: similarityClusters(analyzed, pairs),
	}
}

// collectSimilarityFiles lists the non-excluded files matching include
// under dirs, sorted by project-relative path. Missing dirs are skipped
// silently.
func collectSimilarityFiles(root string, dirs, include, exclude []string) []string {
	var collected []string
	seen := map[string]bool{}
	for _, dir := range dirs {
		abs := dir
		if !filepath.IsAbs(dir) {
			abs = filepath.Join(root, dir)
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			continue
		}
		collectSimilarityDir(root, abs, include, exclude, seen, &collected)
	}
	sort.Strings(collected)
	return collected
}

// collectSimilarityDir walks one directory and appends the files
// matching include but not exclude, tracked by project-relative path.
func collectSimilarityDir(root, abs string, include, exclude []string, seen map[string]bool, collected *[]string) {
	filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] || !matchesAnyGlob(rel, include) || matchesAnyGlob(rel, exclude) {
			return nil
		}
		seen[rel] = true
		*collected = append(*collected, rel)
		return nil
	})
}

// similarityPairs returns all pairs crossing either threshold, strongest
// first. Intersection is computed once per pair.
func similarityPairs(files []similarityFile, jaccardThreshold, containmentThreshold float64) []SimilarityPair {
	var pairs []SimilarityPair
	for i := 0; i < len(files); i++ {
		for j := i + 1; j < len(files); j++ {
			a, b := files[i].shingles, files[j].shingles
			if len(a) == 0 || len(b) == 0 {
				continue
			}
			shared := intersectionCount(a, b)
			jac := float64(shared) / float64(len(a)+len(b)-shared)
			con := float64(shared) / float64(min(len(a), len(b)))
			if jac < jaccardThreshold && con < containmentThreshold {
				continue
			}
			pairs = append(pairs, SimilarityPair{A: files[i].path, B: files[j].path, Jaccard: jac, Containment: con})
		}
	}
	sort.SliceStable(pairs, func(i, j int) bool { return pairs[i].Jaccard > pairs[j].Jaccard })
	return pairs
}

// simUnionFind is union-find over analyzed file paths.
type simUnionFind map[string]string

func (u simUnionFind) add(item string) { u[item] = item }

func (u simUnionFind) find(item string) string {
	root := item
	for u[root] != root {
		root = u[root]
	}
	// Path compression.
	for u[item] != root {
		u[item], item = root, u[item]
	}
	return root
}

func (u simUnionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u[rb] = ra
	}
}

// similarityClusters merges the threshold-crossing pairs transitively
// into clusters, largest first (ties keep scan order). Cluster members
// are sorted (scan order is sorted by path).
func similarityClusters(files []similarityFile, pairs []SimilarityPair) []SimilarityCluster {
	union := simUnionFind{}
	for _, f := range files {
		union.add(f.path)
	}
	for _, pair := range pairs {
		union.union(pair.A, pair.B)
	}
	var multi []SimilarityCluster
	for _, cluster := range similarityClusterGroups(files, union) {
		if len(cluster.Files) < 2 {
			continue
		}
		similarityAttachPairs(&cluster, pairs)
		multi = append(multi, cluster)
	}
	sort.SliceStable(multi, func(i, j int) bool { return len(multi[i].Files) > len(multi[j].Files) })
	return multi
}

// similarityClusterGroups groups the files by union-find root in scan
// order, so cluster order is deterministic.
func similarityClusterGroups(files []similarityFile, union simUnionFind) []SimilarityCluster {
	var clusters []SimilarityCluster
	rootIndex := map[string]int{}
	for _, f := range files {
		root := union.find(f.path)
		if _, ok := rootIndex[root]; !ok {
			rootIndex[root] = len(clusters)
			clusters = append(clusters, SimilarityCluster{})
		}
		clusters[rootIndex[root]].Files = append(clusters[rootIndex[root]].Files, f.path)
	}
	return clusters
}

// similarityAttachPairs appends the pairs whose both endpoints are
// cluster members (pairs are strongest-first).
func similarityAttachPairs(cluster *SimilarityCluster, pairs []SimilarityPair) {
	members := make(map[string]bool, len(cluster.Files))
	for _, f := range cluster.Files {
		members[f] = true
	}
	for _, pair := range pairs {
		if members[pair.A] && members[pair.B] {
			cluster.Pairs = append(cluster.Pairs, pair)
		}
	}
}
