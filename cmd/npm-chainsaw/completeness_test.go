package main

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRaw writes arbitrary bytes to path, creating parents. Used for
// malformed fixtures the typed helper can't express.
func writeRaw(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// gapsByCause indexes gaps by cause for assertions.
func gapsByCause(gaps []Gap) map[string][]Gap {
	m := map[string][]Gap{}
	for _, g := range gaps {
		m[g.Cause] = append(m[g.Cause], g)
	}
	return m
}

// TestScan_DifferentialOracle is the "almost proof" of no false negatives: the
// scanner's found set must EXACTLY equal an independent enumeration of every
// package.json under the root, minus the two documented skips (.git and the
// npm content-v2 blob store). Any package.json the scanner misses — or invents
// — fails this test. Every fixture is a hit (target pkg@*), so "found" and
// "every package.json" are directly comparable.
func TestScan_DifferentialOracle(t *testing.T) {
	root := t.TempDir()

	// Found: package.json scattered across the awkward shapes a real machine
	// has — top-level, nested, hidden-at-root, hidden-deep, deep transitive.
	found := []string{
		"package.json",
		"a/package.json",
		"a/b/c/package.json",
		".hidden/package.json",
		".config/tool/node_modules/pkg/package.json",
		".vscode/extensions/ext/node_modules/pkg/package.json",
		"proj/node_modules/x/node_modules/y/package.json",
		"proj/node_modules/dep/node_modules/.cache/z/package.json",
		"deep/1/2/3/4/5/6/7/package.json",
	}
	// Skipped by design: under .git and under _cacache/content-v2.
	skipped := []string{
		"repo/.git/hooks/package.json",
		"npm/_cacache/content-v2/sha512/aa/bb/package.json",
	}
	for _, r := range append(append([]string{}, found...), skipped...) {
		writePackageJSON(t, filepath.Join(root, r), "pkg", "1.0.0")
	}
	// A non-package.json file that must be ignored entirely.
	writeRaw(t, filepath.Join(root, "a/manifest.json"), `{"name":"pkg","version":"1.0.0"}`)

	hits, _, gaps, err := scan(root, Targets{"pkg": {"*": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(gaps) != 0 {
		t.Fatalf("expected no gaps on a fully readable tree, got %v", gaps)
	}

	got := map[string]bool{}
	for _, h := range hits {
		got[h.Path] = true
	}

	// Oracle: independently walk the tree for every package.json, applying the
	// same documented skips, and compare sets.
	want := map[string]bool{}
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Base(p) != "package.json" {
			return nil
		}
		if oracleSkipped(p) {
			return nil
		}
		want[p] = true
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for p := range want {
		if !got[p] {
			t.Errorf("scanner MISSED a package.json: %s", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("scanner reported a package.json the oracle didn't: %s", p)
		}
	}
	if len(got) != len(found) {
		t.Errorf("found %d package.json, expected %d", len(got), len(found))
	}
}

// oracleSkipped mirrors shouldSkipDir for the differential test: a package.json
// is excluded iff it lives under a .git dir or under _cacache/content-v2.
func oracleSkipped(p string) bool {
	sep := string(filepath.Separator)
	return strings.Contains(p, sep+".git"+sep) ||
		strings.Contains(p, sep+"_cacache"+sep+"content-v2"+sep)
}

// TestScan_GapClassification covers the gap types that don't need chmod: a
// malformed package.json and a lockfile whose format we can't parse are both
// hard gaps, while a valid sibling still produces its hit.
func TestScan_GapClassification(t *testing.T) {
	root := t.TempDir()

	// Valid hit.
	writePackageJSON(t, filepath.Join(root, "proj/node_modules/good/package.json"), "pkg", "1.0.0")
	// Malformed package.json: hard gap, no hit.
	writeRaw(t, filepath.Join(root, "proj/node_modules/bad/package.json"), "{ not json")
	// Nameless manifest: not a hit, not a gap (it's a project file, not an install).
	writeRaw(t, filepath.Join(root, "proj/package.json"), `{"private":true}`)
	// yarn.lock with a package header but no version line: format not understood.
	writeRaw(t, filepath.Join(root, "proj/yarn.lock"), "chalk@^5.0.0:\n  resolution: \"x\"\n")

	hits, _, gaps, err := scan(root, Targets{"pkg": {"*": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(hits) != 1 || hits[0].Name != "pkg" {
		t.Fatalf("want exactly one pkg hit, got %v", hits)
	}

	byCause := gapsByCause(gaps)
	if len(byCause[causeMalformedJSON]) != 1 {
		t.Errorf("want 1 malformed-json gap, got %v", byCause[causeMalformedJSON])
	}
	if len(byCause[causeLockfileParse]) != 1 {
		t.Errorf("want 1 lockfile-unparsed gap, got %v", byCause[causeLockfileParse])
	}
	for _, g := range gaps {
		if g.Severity != sevHard {
			t.Errorf("expected hard gap, got %+v", g)
		}
	}
	if countHard(gaps) != 2 {
		t.Errorf("want 2 hard gaps, got %d (%v)", countHard(gaps), gaps)
	}
}

// TestScan_UnreadableDirSeverity checks that an unreadable directory inside a
// node_modules tree is a hard gap (could hide an install) while one elsewhere
// is soft. Skipped as root, which bypasses permission bits.
func TestScan_UnreadableDirSeverity(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("chmod-based unreadable tests are meaningless as root")
	}
	root := t.TempDir()

	hardDir := filepath.Join(root, "proj/node_modules/locked")
	softDir := filepath.Join(root, "data/locked")
	for _, d := range []string{hardDir, softDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(d, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(d, 0o755) }) // so t.TempDir cleanup can remove it
	}

	_, _, gaps, err := scan(root, Targets{"pkg": {"*": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	var hard, soft int
	for _, g := range gaps {
		if g.Cause != causeUnreadableDir {
			continue
		}
		switch {
		case strings.HasPrefix(g.Path, hardDir):
			if g.Severity == sevHard {
				hard++
			}
		case strings.HasPrefix(g.Path, softDir):
			if g.Severity == sevSoft {
				soft++
			}
		}
	}
	if hard != 1 {
		t.Errorf("want 1 hard unreadable-dir gap inside node_modules, got %d (%v)", hard, gaps)
	}
	if soft != 1 {
		t.Errorf("want 1 soft unreadable-dir gap outside node_modules, got %d (%v)", soft, gaps)
	}
}

// TestScan_SymlinkOutsideRootIsGap verifies the symlink policy: a directory
// symlink escaping the scan root is recorded as a gap (not followed), while one
// resolving back inside the root is not a gap (already walked).
func TestScan_SymlinkOutsideRootIsGap(t *testing.T) {
	root := t.TempDir()
	external := t.TempDir()
	writePackageJSON(t, filepath.Join(external, "evil/package.json"), "pkg", "1.0.0")

	nm := filepath.Join(root, "proj", "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(nm, "linked")); err != nil {
		t.Skip("symlinks unsupported on this platform:", err)
	}
	// An in-root real package + a symlink pointing back inside the root.
	writePackageJSON(t, filepath.Join(root, "real/package.json"), "pkg", "1.0.0")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(nm, "inroot")); err != nil {
		t.Fatal(err)
	}

	hits, _, gaps, err := scan(root, Targets{"pkg": {"*": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}

	for _, h := range hits {
		if strings.HasPrefix(h.Path, external) {
			t.Errorf("scanner followed a symlink outside the root: %s", h.Path)
		}
	}
	var sym []Gap
	for _, g := range gaps {
		if g.Cause == causeSymlinkSkipped {
			sym = append(sym, g)
		}
	}
	if len(sym) != 1 {
		t.Fatalf("want exactly 1 symlink gap (the external one), got %v", sym)
	}
	if sym[0].Severity != sevHard {
		t.Errorf("symlink inside node_modules should be hard, got %+v", sym[0])
	}
}

// TestPrintHuman_CompletenessIsLast pins the report order: the completeness
// verdict comes after the scanned-breakdown and the per-package summary, so the
// last thing the user reads is "is this trustworthy?".
func TestPrintHuman_CompletenessIsLast(t *testing.T) {
	var buf bytes.Buffer
	gaps := []Gap{{Path: "/x/node_modules/bad/package.json", Cause: causeMalformedJSON, Severity: sevHard}}
	printHuman(&buf, nil, Targets{"pkg": {"1.0.0": true}}, Counts{PackageJSON: 2}, gaps, time.Second, "/root", false, false, false)
	out := buf.String()

	iScanned := strings.Index(out, "Scanned")
	iSummary := strings.Index(out, "packages HIT")
	iVerdict := strings.Index(out, "INCOMPLETE")
	if iScanned < 0 || iSummary < 0 || iVerdict < 0 {
		t.Fatalf("missing a section (scanned=%d summary=%d verdict=%d):\n%s",
			iScanned, iSummary, iVerdict, out)
	}
	if !(iScanned < iSummary && iSummary < iVerdict) {
		t.Errorf("sections out of order (scanned=%d summary=%d verdict=%d):\n%s",
			iScanned, iSummary, iVerdict, out)
	}
}

// TestRunCLI_ExitCodes pins the verdict precedence end-to-end: 0 clean,
// 1 hits, 3 incomplete (hard gap, no hit). Uses a malformed package.json for
// the hard gap so the test is root-safe (no chmod).
func TestRunCLI_ExitCodes(t *testing.T) {
	dir := t.TempDir()
	list := filepath.Join(dir, "list.txt")
	writeRaw(t, list, "pkg@1.0.0\n")

	clean := t.TempDir()
	writePackageJSON(t, filepath.Join(clean, "ok/package.json"), "safe", "9.9.9")
	if code := runCLI([]string{list, clean, "--no-cache"}); code != 0 {
		t.Errorf("clean scan: exit %d, want 0", code)
	}

	hitDir := t.TempDir()
	writePackageJSON(t, filepath.Join(hitDir, "x/package.json"), "pkg", "1.0.0")
	if code := runCLI([]string{list, hitDir, "--no-cache"}); code != 1 {
		t.Errorf("hit scan: exit %d, want 1", code)
	}

	gapDir := t.TempDir()
	writeRaw(t, filepath.Join(gapDir, "n/node_modules/bad/package.json"), "{ broken")
	if code := runCLI([]string{list, gapDir, "--no-cache"}); code != 3 {
		t.Errorf("incomplete scan: exit %d, want 3", code)
	}
}
