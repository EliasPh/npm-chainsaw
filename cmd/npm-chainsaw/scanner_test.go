package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writePackageJSON is a tiny helper that creates the dir tree and writes a
// minimal package.json at path.
func writePackageJSON(t *testing.T, path, name, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{"name":"` + name + `","version":"` + version + `"}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScan_FindsHitsAndAppliesSkipRules(t *testing.T) {
	root := t.TempDir()

	// Should be found: top-level project.
	writePackageJSON(t, filepath.Join(root, "proj/package.json"), "chalk", "5.6.1")

	// Should be found: deeply nested transitive in node_modules.
	writePackageJSON(t,
		filepath.Join(root, "proj/node_modules/foo/node_modules/chalk/package.json"),
		"chalk", "5.6.1")

	// Should NOT be found: version doesn't match.
	writePackageJSON(t, filepath.Join(root, "proj/node_modules/safe/package.json"),
		"chalk", "4.0.0")

	// Should NOT be found: inside .git/.
	writePackageJSON(t, filepath.Join(root, "proj/.git/hooks/package.json"),
		"chalk", "5.6.1")

	// Should NOT be found: inside a hidden dir at the scan root.
	writePackageJSON(t, filepath.Join(root, ".hidden/package.json"),
		"chalk", "5.6.1")

	// Should NOT be found: inside a simulated npm content store.
	writePackageJSON(t, filepath.Join(root, "fake-npm/_cacache/content-v2/p/package.json"),
		"chalk", "5.6.1")

	// Wildcard match: any version of suspicious-pkg counts.
	writePackageJSON(t, filepath.Join(root, "proj/node_modules/suspicious-pkg/package.json"),
		"suspicious-pkg", "9.9.9")

	targets := Targets{
		"chalk":          {"5.6.1": true},
		"suspicious-pkg": {"*": true},
	}

	hits, counts, err := scan(root, targets, nil)
	if err != nil {
		t.Fatal(err)
	}
	if counts.Total() == 0 {
		t.Errorf("inspected count should be > 0, got %+v", counts)
	}

	// Compare hits by their relative path for stable assertions.
	got := make([]string, 0, len(hits))
	for _, h := range hits {
		rel, _ := filepath.Rel(root, h.Path)
		got = append(got, h.Name+"@"+h.Version+"|"+rel)
	}
	sort.Strings(got)

	want := []string{
		"chalk@5.6.1|proj/node_modules/foo/node_modules/chalk/package.json",
		"chalk@5.6.1|proj/package.json",
		"suspicious-pkg@9.9.9|proj/node_modules/suspicious-pkg/package.json",
	}
	if len(got) != len(want) {
		t.Fatalf("hits = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("hit[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestScan_FindsLockfileHits(t *testing.T) {
	root := t.TempDir()
	lock := filepath.Join(root, "proj/package-lock.json")
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		t.Fatal(err)
	}
	body := `{
      "lockfileVersion": 3,
      "packages": {
        "node_modules/chalk": {"version": "5.6.1"},
        "node_modules/safe":  {"version": "1.0.0"}
      }
    }`
	if err := os.WriteFile(lock, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	hits, _, err := scan(root, Targets{"chalk": {"5.6.1": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Name != "chalk" || hits[0].Kind != "lockfile" {
		t.Errorf("got %v, want one chalk lockfile hit", hits)
	}
}

func TestScan_IgnoresMalformedJSON(t *testing.T) {
	root := t.TempDir()
	bad := filepath.Join(root, "broken/package.json")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	hits, _, err := scan(root, Targets{"chalk": {"5.6.1": true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Errorf("expected no hits from malformed JSON, got %v", hits)
	}
}
