package main

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// scan walks root and returns hits against targets, plus the number of
// files actually inspected (used for the run-summary footer).
//
// The walk runs on the calling goroutine and only does skip-rule checks and
// filename matching. Interesting paths go to a pool of runtime.NumCPU()
// workers that do the ReadFile + parse + match. Hits are appended under a
// mutex. The set of hits is the same as a single-threaded walk, just in
// non-deterministic order; output.go sorts before display.
//
// Skip rules during the walk:
//   - .git directories anywhere
//   - any _cacache/content-v2 directory (the npm tarball store, gigabytes
//     of compressed packages; the index is enough to answer "ever fetched")
//   - hidden dirs (".something") at the scan root only. This keeps a
//     default "scan $HOME" from descending into ~/.Trash, ~/.cache, etc.
//     Hidden dirs deeper down (e.g. node_modules/.bin) are walked normally.
//   - symbolic links: filepath.WalkDir doesn't follow them by default,
//     which is what we want.
//
// Individual path errors (permission denied, etc.) are dropped to keep the
// walk going. A future --verbose mode will surface them.
//
// progress is an optional shared counter the caller can read concurrently
// (e.g. from a progress-display goroutine). Pass nil if not needed.
func scan(root string, targets Targets, progress *atomic.Int64) ([]Hit, int, error) {
	counter := progress
	if counter == nil {
		counter = new(atomic.Int64)
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, 0, err
	}

	// Buffered so the walker can stay ahead of the workers without blocking
	// on every send. 256 is plenty in practice.
	jobs := make(chan string, 256)

	var (
		wg     sync.WaitGroup
		hitsMu sync.Mutex
		hits   []Hit
	)
	for i := 0; i < runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range jobs {
				if found := processFile(path, targets); len(found) > 0 {
					hitsMu.Lock()
					hits = append(hits, found...)
					hitsMu.Unlock()
				}
			}
		}()
	}

	walkErr := filepath.WalkDir(absRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if shouldSkipDir(path, d.Name(), absRoot) {
				return fs.SkipDir
			}
			return nil
		}
		if isInterestingFile(d.Name()) {
			counter.Add(1)
			jobs <- path
		}
		return nil
	})
	close(jobs)
	wg.Wait()
	return hits, int(counter.Load()), walkErr
}

// isInterestingFile reports whether a filename is one we want to read.
func isInterestingFile(name string) bool {
	switch name {
	case "package.json",
		"package-lock.json", "npm-shrinkwrap.json",
		"yarn.lock", "pnpm-lock.yaml":
		return true
	}
	return false
}

// processFile reads one file and returns any matching hits. Pure with
// respect to shared state, so it's safe to call from many goroutines.
func processFile(path string, targets Targets) []Hit {
	switch filepath.Base(path) {
	case "package.json":
		if h, ok := matchPackageJSON(path, targets, "package.json"); ok {
			return []Hit{h}
		}
	case "package-lock.json", "npm-shrinkwrap.json", "yarn.lock", "pnpm-lock.yaml":
		var found []Hit
		for _, p := range parseLockfile(path) {
			if h, ok := matchPair(p, path, targets); ok {
				found = append(found, h)
			}
		}
		return found
	}
	return nil
}

// shouldSkipDir applies the walk skip rules. Kept as a small pure helper so
// it's straightforward to unit-test.
func shouldSkipDir(path, name, absRoot string) bool {
	if name == ".git" {
		return true
	}
	// "_cacache/content-v2" is the npm tarball store. Walking it once took
	// 20+ minutes in an earlier bash attempt; never read this directory.
	if name == "content-v2" && filepath.Base(filepath.Dir(path)) == "_cacache" {
		return true
	}
	// Hidden directories at the scan root only. Deeper hidden dirs are fine.
	if strings.HasPrefix(name, ".") && filepath.Dir(path) == absRoot {
		return true
	}
	return false
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

// matchPackageJSON reads name+version from a package.json and reports a hit
// if the package is in targets. The kind argument labels the source (e.g.
// "package.json", "pnpm-store", "global") for downstream display. Read
// errors and malformed JSON are treated as "no match"; better to miss a
// corrupt file than crash the scan.
func matchPackageJSON(path string, targets Targets, kind string) (Hit, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Hit{}, false
	}
	// Only the two fields we need; json.Unmarshal silently ignores the rest.
	var p struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &p); err != nil {
		return Hit{}, false
	}
	if p.Name == "" {
		return Hit{}, false
	}
	versions, ok := targets[p.Name]
	if !ok {
		return Hit{}, false
	}
	if versions[p.Version] || versions["*"] {
		return Hit{Name: p.Name, Version: p.Version, Path: path, Kind: kind}, true
	}
	return Hit{}, false
}
