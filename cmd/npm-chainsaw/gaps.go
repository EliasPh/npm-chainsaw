package main

import (
	"path/filepath"
	"sort"
	"strings"
)

// Gap is a location the scan could not fully cover. Gaps are first-class: a
// run is "provably clean" only when it has zero hits AND zero hard gaps.
// Silently dropping these would make the completeness promise unfalsifiable.
type Gap struct {
	Path     string // absolute path of the location we couldn't cover
	Cause    string // why (one of the cause* constants below)
	Severity string // sevHard or sevSoft
}

// Severity levels. A hard gap could hide an affected package, so it makes the
// scan "incomplete" (exit 3). A soft gap is reported for transparency but does
// not change the verdict.
const (
	sevHard = "hard"
	sevSoft = "soft"
)

// Causes. These are stable strings emitted in JSON output, so don't rename
// them lightly.
const (
	causeUnreadableDir   = "unreadable-dir"    // a directory we couldn't list
	causeUnreadableFile  = "unreadable-file"   // a package.json we couldn't read
	causeMalformedJSON   = "malformed-json"    // a package.json with unparseable JSON
	causeLockfileParse   = "lockfile-unparsed" // a lockfile whose format we didn't understand
	causeSymlinkSkipped  = "symlink-skipped"   // a dir symlink resolving outside the scan root
	causeCacheUnreadable = "cache-unreadable"  // a cache file/dir we couldn't read
)

// nodeModulesContext reports whether path is part of a node_modules tree. Used
// to grade the severity of an unreadable directory or skipped symlink: failing
// inside node_modules means we were mid-package-tree and may have missed an
// install (hard); failing elsewhere is just an unreachable corner (soft).
func nodeModulesContext(path string) bool {
	sep := string(filepath.Separator)
	return filepath.Base(path) == "node_modules" ||
		strings.Contains(path, sep+"node_modules"+sep) ||
		strings.HasSuffix(path, sep+"node_modules")
}

// dirSeverity grades a directory/symlink failure by whether it sits in a
// package tree.
func dirSeverity(path string) string {
	if nodeModulesContext(path) {
		return sevHard
	}
	return sevSoft
}

// sortGaps orders gaps hard-first, then by path, for stable display. It also
// drops exact (path, cause) duplicates, which the parallel scan can produce if
// a location is reached more than once.
func sortGaps(gaps []Gap) []Gap {
	seen := make(map[string]bool, len(gaps))
	out := gaps[:0]
	for _, g := range gaps {
		key := g.Path + "|" + g.Cause
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool {
		ih, jh := out[i].Severity == sevHard, out[j].Severity == sevHard
		if ih != jh {
			return ih // hard gaps first
		}
		return out[i].Path < out[j].Path
	})
	return out
}

// countHard returns how many of the gaps are hard. Zero hard gaps is the
// condition for a "complete" scan.
func countHard(gaps []Gap) int {
	n := 0
	for _, g := range gaps {
		if g.Severity == sevHard {
			n++
		}
	}
	return n
}
