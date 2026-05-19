package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNameFromYarnBerryFilename(t *testing.T) {
	cases := []struct {
		in            string
		name, version string
		ok            bool
	}{
		{"chalk-npm-5.6.1-deadbeef10-cf4c61a9bd.zip", "chalk", "5.6.1", true},
		{"@types-node-npm-18.0.0-aaaaaaaaaa-bbbbbbbbbb.zip", "@types/node", "18.0.0", true},
		{"react-dom-npm-18.2.0-1a39f9e7d9-cf4c61a9bd.zip", "react-dom", "18.2.0", true},
		{"foo-npm-1.0.0-beta.1-aaaaaaaaaa-bbbbbbbbbb.zip", "foo", "1.0.0-beta.1", true},
		{"not-a-package.zip", "", "", false},
		{"random.txt", "", "", false},
	}
	for _, c := range cases {
		name, version, ok := nameFromYarnBerryFilename(c.in)
		if ok != c.ok || name != c.name || version != c.version {
			t.Errorf("for %q got (%q, %q, %v) want (%q, %q, %v)",
				c.in, name, version, ok, c.name, c.version, c.ok)
		}
	}
}

func TestScanNpmCacheIndex(t *testing.T) {
	root := t.TempDir()
	// npm splits the index by hash prefix; one nested file is enough.
	leaf := filepath.Join(root, "ab", "cd")
	if err := os.MkdirAll(leaf, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "" +
		"deadbeef\t{\"key\":\"make-fetch-happen:request-cache:https://registry.npmjs.org/chalk/-/chalk-5.6.1.tgz\"}\n" +
		"cafebabe\t{\"key\":\"...https://registry.npmjs.org/@ctrl/tinycolor/-/tinycolor-4.1.2.tgz\"}\n" +
		"feedface\t{\"key\":\"...https://registry.npmjs.org/lodash/-/lodash-4.17.21.tgz\"}\n"
	if err := os.WriteFile(filepath.Join(leaf, "ledger"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	targets := Targets{
		"chalk":           {"5.6.1": true},
		"@ctrl/tinycolor": {"*": true},
		// lodash deliberately not listed
	}
	hits, entries := scanNpmCacheIndex(root, targets)
	if len(hits) != 2 {
		t.Fatalf("got %d hits, want 2: %v", len(hits), hits)
	}
	if entries != 3 {
		t.Errorf("entries scanned = %d, want 3 (the 3 lines that look like ledger entries)", entries)
	}
	found := map[string]bool{}
	for _, h := range hits {
		if h.Kind != "npm-cache" {
			t.Errorf("hit kind = %q, want npm-cache", h.Kind)
		}
		found[h.Name+"@"+h.Version] = true
	}
	if !found["chalk@5.6.1"] || !found["@ctrl/tinycolor@4.1.2"] {
		t.Errorf("missing expected hits: %v", found)
	}
}

// TestScanCaches_AcrossSources builds a synthetic home dir containing one
// hit per cache type and checks each source reports through scanCaches.
// Assertions are additive: extras from real system paths (e.g. a global
// install at /usr/local/lib/node_modules) won't break the test.
func TestScanCaches_AcrossSources(t *testing.T) {
	home := t.TempDir()

	// 1. npm cache index entry
	npmIdx := filepath.Join(home, ".npm", "_cacache", "index-v5", "ab")
	if err := os.MkdirAll(npmIdx, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(npmIdx, "ledger"),
		[]byte("x\t{\"key\":\"https://reg/chalk/-/chalk-5.6.1.tgz\"}\n"), 0o644)

	// 2. pnpm store (macOS path is tried first; populating just that one
	// is fine because the scanner silently no-ops on missing alternatives).
	pnpmPkg := filepath.Join(home, "Library", "pnpm", "store", "v3", "files", "chalk", "package.json")
	if err := os.MkdirAll(filepath.Dir(pnpmPkg), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(pnpmPkg, []byte(`{"name":"chalk","version":"5.6.1"}`), 0o644)

	// 3. yarn berry cache zip
	berry := filepath.Join(home, ".yarn", "berry", "cache")
	if err := os.MkdirAll(berry, 0o755); err != nil {
		t.Fatal(err)
	}
	zipPath := filepath.Join(berry, "chalk-npm-5.6.1-deadbeef10-cf4c61a9bd.zip")
	os.WriteFile(zipPath, nil, 0o644)

	hits, counts := scanCaches(home, Targets{"chalk": {"5.6.1": true}})
	if counts.Total() == 0 {
		t.Errorf("counts total should be > 0, got %+v", counts)
	}

	kinds := map[string]int{}
	for _, h := range hits {
		kinds[h.Kind]++
	}
	for _, want := range []string{"npm-cache", "pnpm-store", "yarn-cache"} {
		if kinds[want] == 0 {
			t.Errorf("expected at least one hit of kind %q, got %v", want, kinds)
		}
	}
}
