package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
)

// Hit is a single match of a target package at a specific path on disk.
type Hit struct {
	Name    string // package name (e.g. "@ctrl/tinycolor")
	Version string // version found on disk
	Path    string // absolute path to the file that matched
	Kind    string // "package.json", "lockfile", "npm-cache", "yarn-cache", "pnpm-store"
}

// Counts tracks how many files / entries each source inspected. The walk
// fills PackageJSON and Lockfile; scanCaches fills the rest. cli.go merges
// the two via Add so the report has one consolidated breakdown.
type Counts struct {
	PackageJSON int // package.json files read during the main walk
	Lockfile    int // lockfile files read during the main walk
	NpmCache    int // npm cache index ledger entries inspected
	PnpmStore   int // package.json files in the pnpm store
	YarnCache   int // yarn berry zip filenames + v1 cache package.json
	Global      int // package.json files in global install locations
}

// Add accumulates b into a. Used to merge per-pass counts in the CLI.
func (a *Counts) Add(b Counts) {
	a.PackageJSON += b.PackageJSON
	a.Lockfile += b.Lockfile
	a.NpmCache += b.NpmCache
	a.PnpmStore += b.PnpmStore
	a.YarnCache += b.YarnCache
	a.Global += b.Global
}

// Total returns the sum across every source.
func (c Counts) Total() int {
	return c.PackageJSON + c.Lockfile + c.NpmCache + c.PnpmStore + c.YarnCache + c.Global
}

// scan walks root and returns hits against targets, a count of files
// inspected (for the footer), and any gaps - locations it could not fully
// cover (see gaps.go). A run is only "provably clean" with zero hits AND zero
// hard gaps.
//
// The walker goroutine does skip checks, gap recording, and filename matching;
// matching paths go to a pool of runtime.NumCPU() workers that read, parse, and
// match. Hits and worker gaps are collected under mutexes, so order is
// non-deterministic; output.go sorts.
//
// The walk is intentionally exhaustive - hidden dirs included, so a scan can't
// miss an install (see shouldSkipDir for the only two exceptions). Symlinks
// aren't followed (filepath.WalkDir's default); a directory symlink whose
// target escapes the scan root is recorded as a gap rather than silently
// dropped (symlinkGap).
func scan(root string, targets Targets, progress *atomic.Int64) ([]Hit, Counts, []Gap, error) {
	counter := progress
	if counter == nil {
		counter = new(atomic.Int64)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, Counts{}, nil, err
	}
	// Resolved form of the root, so symlink targets (which EvalSymlinks
	// returns resolved, e.g. /private/var on macOS) can be compared against it
	// without falsely flagging in-root links as escaping.
	realRoot, evErr := filepath.EvalSymlinks(absRoot)
	if evErr != nil {
		realRoot = absRoot
	}

	// Buffered so the walker can stay ahead of the workers without blocking
	// on every send. 256 is plenty in practice.
	jobs := make(chan string, 256)

	var (
		wg         sync.WaitGroup
		resultsMu  sync.Mutex
		hits       []Hit
		workerGaps []Gap
	)
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				foundHits, foundGaps := processFile(path, targets)
				if len(foundHits) > 0 || len(foundGaps) > 0 {
					resultsMu.Lock()
					hits = append(hits, foundHits...)
					workerGaps = append(workerGaps, foundGaps...)
					resultsMu.Unlock()
				}
			}
		}()
	}

	// counts and walkGaps are touched only by the walker (single goroutine),
	// so plain values are race-free without coordination.
	var counts Counts
	var walkGaps []Gap
	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// A directory (or the root) we couldn't read is a gap, not a
			// silent skip - it may have held an install we never saw.
			g := Gap{Path: path, Cause: causeUnreadableDir, Severity: dirSeverity(path)}
			if d != nil && !d.IsDir() {
				g.Cause = causeUnreadableFile
			}
			walkGaps = append(walkGaps, g)
			if d == nil || d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(path, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		// A directory symlink that resolves outside the scan root is content
		// the walk will never reach; record it. In-root targets (e.g. pnpm's
		// top-level links into .pnpm) are already walked, so they're skipped.
		if d.Type()&fs.ModeSymlink != 0 {
			if g, ok := symlinkGap(path, absRoot, realRoot); ok {
				walkGaps = append(walkGaps, g)
			}
			return nil
		}
		switch d.Name() {
		case "package.json":
			counts.PackageJSON++
			counter.Add(1)
			jobs <- path
		case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml":
			counts.Lockfile++
			counter.Add(1)
			jobs <- path
		}
		return nil
	})
	close(jobs)
	wg.Wait()
	gaps := append(walkGaps, workerGaps...)
	return hits, counts, gaps, walkErr
}

// processFile reads one file and returns matching hits plus any gaps (an
// unreadable/corrupt package.json, or a lockfile whose format we couldn't
// parse). Pure with respect to shared state, so it's safe from many goroutines.
func processFile(path string, targets Targets) ([]Hit, []Gap) {
	switch filepath.Base(path) {
	case "package.json":
		h, ok, gap := matchPackageJSON(path, targets, "package.json")
		var hits []Hit
		var gaps []Gap
		if ok {
			hits = []Hit{h}
		}
		if gap != nil {
			gaps = []Gap{*gap}
		}
		return hits, gaps
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml":
		pairs, gap := parseLockfile(path)
		var hits []Hit
		for _, p := range pairs {
			if h, ok := matchPair(p, path, targets); ok {
				hits = append(hits, h)
			}
		}
		var gaps []Gap
		if gap != nil {
			gaps = []Gap{*gap}
		}
		return hits, gaps
	}
	return nil, nil
}

// symlinkGap decides whether a directory symlink at path is a gap. It's a gap
// only if it resolves to a directory outside the scan root: that target is
// content the no-follow walk will never reach. Links resolving inside the root
// are already covered, file/dangling links can't hide a package tree.
func symlinkGap(path, absRoot, realRoot string) (Gap, bool) {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return Gap{}, false // dangling or unreadable link: don't spam
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.IsDir() {
		return Gap{}, false // only directory symlinks can hide a package tree
	}
	if withinRoot(absRoot, real) || withinRoot(realRoot, real) {
		return Gap{}, false // target is under the scan root: already walked
	}
	return Gap{Path: path, Cause: causeSymlinkSkipped, Severity: dirSeverity(path)}, true
}

// matchPair turns a (name, version) pair from a lockfile into a Hit if it
// matches any target. Same logic as matchPackageJSON but for the parsed
// lockfile pairs rather than a package.json file.
func matchPair(p nameVersionPair, path string, targets Targets) (Hit, bool) {
	versions, ok := targets[p.name]
	if !ok {
		return Hit{}, false
	}
	if versions[p.version] || versions["*"] {
		return Hit{Name: p.name, Version: p.version, Path: path, Kind: "lockfile"}, true
	}
	return Hit{}, false
}

// matchPackageJSON reads name+version from a package.json and reports a hit if
// the package is in targets. The kind argument labels the source (e.g.
// "package.json", "pnpm-store", "global") for downstream display.
//
// The third return value is a hard gap when the file exists but we couldn't
// turn it into an identity: an unreadable file or malformed JSON could be the
// affected package, so we surface it rather than silently treating it as clean.
// A valid package.json with no "name" is a project/workspace manifest, not an
// install, so it's neither a hit nor a gap.
func matchPackageJSON(path string, targets Targets, kind string) (Hit, bool, *Gap) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Hit{}, false, &Gap{Path: path, Cause: causeUnreadableFile, Severity: sevHard}
	}
	// Only the two fields we need; json.Unmarshal silently ignores the rest.
	var p struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Hit{}, false, &Gap{Path: path, Cause: causeMalformedJSON, Severity: sevHard}
	}
	if p.Name == "" {
		return Hit{}, false, nil
	}
	versions, ok := targets[p.Name]
	if !ok {
		return Hit{}, false, nil
	}
	if versions[p.Version] || versions["*"] {
		return Hit{Name: p.Name, Version: p.Version, Path: path, Kind: kind}, true, nil
	}
	return Hit{}, false, nil
}

// shouldSkipDir reports the only two directories the walk skips: .git (VCS
// internals) and npm's _cacache/content-v2 tarball store (compressed blobs,
// no readable package.json - "ever fetched" comes from the cache index).
func shouldSkipDir(path, name string) bool {
	if name == ".git" {
		return true
	}
	if name == "content-v2" && filepath.Base(filepath.Dir(path)) == "_cacache" {
		return true
	}
	return false
}
