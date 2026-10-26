package main

import (
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"sort"
	"strings"
)

// Mutation cache (port of crap4dart 16d9c4c). Killed-stays-killed:
// per-method outcomes persist to <projectRoot>/.crap-mutation-cache.json
// keyed by method identity (file + method name) plus a digest of the
// method's exact source span. On later runs, covered mutants of unchanged
// methods with a matching site identity that were previously killed are
// replayed, not re-executed; survivors always re-execute; coverage gating
// is re-derived from the run's fresh coverage profile. A meta mismatch
// (tool, operators, test_args) wipes the cache.
const (
	mutationCacheFileName = ".crap-mutation-cache.json"

	// mutationCacheVersion is the cache envelope version; a mismatch
	// discards the file.
	mutationCacheVersion = 1

	// mutationCacheTool is the cache meta's tool identity. crap4go carries
	// no runtime version constant, so this pins the mutation gate's
	// semantics instead: bump it whenever mutation-gate behavior changes.
	mutationCacheTool = "crap4go 0.9.0"
)

// fnv1a64Hex returns FNV-1a over text as 16 hex chars — standard library
// hash, stable across platforms and runs.
func fnv1a64Hex(text []byte) string {
	h := fnv.New64a()
	h.Write(text)
	return fmt.Sprintf("%016x", h.Sum64())
}

// mutationCacheMeta is the mutation-relevant configuration fingerprint
// stored in the cache envelope; any mismatch wipes the cache (full
// re-execution) because cached outcomes were produced under different
// semantics.
type mutationCacheMeta struct {
	Tool      string `json:"tool"`
	Operators string `json:"operators"`
	TestArgs  string `json:"test_args"`
}

// currentMutationCacheMeta is the meta of this tool version and
// operators/testArgs combo.
func currentMutationCacheMeta(categories map[string]bool, testArgs []string) mutationCacheMeta {
	names := make([]string, 0, len(categories))
	for name := range categories {
		names = append(names, name)
	}
	sort.Strings(names)
	return mutationCacheMeta{
		Tool:      mutationCacheTool,
		Operators: strings.Join(names, ","),
		TestArgs:  strings.Join(testArgs, ","),
	}
}

// matches reports whether other was produced under identical semantics.
func (m mutationCacheMeta) matches(other mutationCacheMeta) bool {
	return m == other
}

// mutationMethodRef is the cache identity of one method: the lookup key,
// the digest of the method's exact source span, and the method's start
// offset (site offsets are stored relative to it, so an edit in an earlier
// method never shifts the identity of later methods' sites).
type mutationMethodRef struct {
	key    string
	digest string
	start  int
}

// mutationCacheKey is the stable cache key of a method: project-relative
// file path plus its (receiver-qualified) name — two methods may share a
// name across files, the file path keeps them distinct.
func mutationCacheKey(relPath, methodName string) string {
	return relPath + "#" + methodName
}

// cachedMutationOutcome is one cached per-site outcome (killed, survived
// or timeout).
type cachedMutationOutcome struct {
	// Offset of the replaced span relative to the containing method's
	// start (stable across edits elsewhere in the file).
	Offset   int    `json:"offset"`
	Line     int    `json:"line"`
	Category string `json:"category"`
	Original string `json:"original"`
	Mutant   string `json:"mutant"`
	Status   string `json:"status"`
}

// cachedMutationMethod holds one method's digest and per-site outcomes of
// the last run that executed it.
type cachedMutationMethod struct {
	Digest   string                  `json:"digest"`
	Outcomes []cachedMutationOutcome `json:"outcomes"`
}

// mutationFreshMethod accumulates one method's outcomes during a run
// before they are merged into the cache.
type mutationFreshMethod struct {
	digest   string
	outcomes []cachedMutationOutcome
}

// mutationCache is the digest-incremental killed-stays-killed cache: only
// killed outcomes are replayable, survivors are always re-executed.
type mutationCache struct {
	Version int                             `json:"version"`
	Meta    mutationCacheMeta               `json:"meta"`
	Methods map[string]cachedMutationMethod `json:"methods"`
}

// freshMutationCache returns an empty cache under meta.
func freshMutationCache(meta mutationCacheMeta) *mutationCache {
	return &mutationCache{Version: mutationCacheVersion, Meta: meta, Methods: map[string]cachedMutationMethod{}}
}

// replayKill returns the cached killed outcome matching site when the
// stored digest equals ref's; ok=false means the mutant must be executed
// (changed method, unknown site, or a survivor).
func (c *mutationCache) replayKill(ref mutationMethodRef, site MutationSite) (cachedMutationOutcome, bool) {
	method, ok := c.Methods[ref.key]
	if !ok || method.Digest != ref.digest {
		return cachedMutationOutcome{}, false
	}
	for _, o := range method.Outcomes {
		if o.Status == "killed" && cachedSiteMatches(o, ref, site) {
			return o, true
		}
	}
	return cachedMutationOutcome{}, false
}

// cachedSiteMatches reports whether a cached outcome is the stored record
// of exactly this site (method-relative offset, category, original, mutant).
func cachedSiteMatches(o cachedMutationOutcome, ref mutationMethodRef, site MutationSite) bool {
	return o.Offset == site.Offset-ref.start && o.Category == site.Category &&
		o.Original == site.Original && o.Mutant == site.Mutant
}

// mergeRun merges one run's fresh outcomes into the cache: methods seen
// this run get their current digest plus the union of fresh and
// still-valid old outcomes; methods absent from liveKeys (deleted or
// excluded files) are pruned.
func (c *mutationCache) mergeRun(fresh map[string]mutationFreshMethod, liveKeys map[string]bool) {
	for key := range c.Methods {
		if !liveKeys[key] {
			delete(c.Methods, key)
		}
	}
	for key, entry := range fresh {
		c.Methods[key] = cachedMutationMethod{
			Digest:   entry.digest,
			Outcomes: append(entry.outcomes, stillValid(c.Methods[key], entry.outcomes)...),
		}
	}
}

// stillValid keeps a method's old outcomes that no fresh outcome already
// supersedes (same site identity).
func stillValid(old cachedMutationMethod, outcomes []cachedMutationOutcome) []cachedMutationOutcome {
	var kept []cachedMutationOutcome
	for _, o := range old.Outcomes {
		if !sameCachedSite(o, outcomes) {
			kept = append(kept, o)
		}
	}
	return kept
}

// sameCachedSite reports whether outcomes already contain an entry with o's
// site identity.
func sameCachedSite(o cachedMutationOutcome, outcomes []cachedMutationOutcome) bool {
	for _, fresh := range outcomes {
		if fresh.Offset == o.Offset && fresh.Category == o.Category &&
			fresh.Original == o.Original && fresh.Mutant == o.Mutant {
			return true
		}
	}
	return false
}

// save writes the cache (pretty-printed) to path.
func (c *mutationCache) save(path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), writableFilePerm)
}

// loadMutationCache loads the cache at path under meta. A missing, corrupt,
// outdated or meta-mismatched file yields a fresh cache — a full
// re-execution under the current semantics.
func loadMutationCache(path string, meta mutationCacheMeta) *mutationCache {
	data, err := os.ReadFile(path)
	if err != nil {
		return freshMutationCache(meta)
	}
	var decoded mutationCache
	if err := json.Unmarshal(data, &decoded); err != nil {
		return freshMutationCache(meta)
	}
	if decoded.Version != mutationCacheVersion || !decoded.Meta.matches(meta) {
		return freshMutationCache(meta)
	}
	if decoded.Methods == nil {
		decoded.Methods = map[string]cachedMutationMethod{}
	}
	decoded.Version = mutationCacheVersion
	decoded.Meta = meta
	return &decoded
}

// mutationCacheSession is everything the engine needs to consult and
// update the on-disk cache during one gate run.
type mutationCacheSession struct {
	cache       *mutationCache
	cacheFile   string
	siteMethods map[MutationSite]mutationMethodRef
	liveKeys    map[string]bool
}

// ref returns the cache identity bound to site; ok=false when the site has
// none (or caching is off) — such sites are never cached.
func (s *mutationCacheSession) ref(site MutationSite) (mutationMethodRef, bool) {
	if s == nil {
		return mutationMethodRef{}, false
	}
	ref, ok := s.siteMethods[site]
	return ref, ok
}
