package main

import (
	"os"
	"strings"
	"testing"
)

func TestParseTargets_BasicWithComments(t *testing.T) {
	in := `# header comment
# source: example

chalk@5.6.1
`
	got, count, err := parseTargets(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1", count)
	}
	if !got["chalk"]["5.6.1"] {
		t.Errorf("chalk@5.6.1 not found: %v", got)
	}
}

func TestParseTargets_ScopedPackage(t *testing.T) {
	got, _, err := parseTargets(strings.NewReader("@ctrl/tinycolor@4.1.2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !got["@ctrl/tinycolor"]["4.1.2"] {
		t.Errorf("scoped package not parsed correctly: %v", got)
	}
}

func TestParseTargets_Wildcard(t *testing.T) {
	got, _, err := parseTargets(strings.NewReader("suspicious-pkg@*\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !got["suspicious-pkg"]["*"] {
		t.Errorf("wildcard not parsed: %v", got)
	}
}

func TestParseTargets_DedupesRepeats(t *testing.T) {
	_, count, err := parseTargets(strings.NewReader("chalk@5.6.1\nchalk@5.6.1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (duplicates should not double-count)", count)
	}
}

func TestParseTargets_CommaSeparatedVersions(t *testing.T) {
	got, count, err := parseTargets(strings.NewReader("wot-api@0.8.1,0.8.2,0.8.3,0.8.4\n"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 4 {
		t.Errorf("count = %d, want 4", count)
	}
	for _, v := range []string{"0.8.1", "0.8.2", "0.8.3", "0.8.4"} {
		if !got["wot-api"][v] {
			t.Errorf("missing wot-api@%s", v)
		}
	}
}

func TestParseTargets_CommaWhitespaceAndEmpty(t *testing.T) {
	// Trailing comma and extra whitespace should be tolerated, not error.
	got, count, err := parseTargets(strings.NewReader("@scope/name@ 1.0.0 , 2.0.0 ,,\n"))
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Errorf("count = %d, want 2", count)
	}
	if !got["@scope/name"]["1.0.0"] || !got["@scope/name"]["2.0.0"] {
		t.Errorf("scoped comma-list not parsed correctly: %v", got)
	}
}

func TestParseTargets_AllEmptyVersionsErrors(t *testing.T) {
	// Only commas after @ → no real versions → must error so typos surface.
	if _, _, err := parseTargets(strings.NewReader("wot-api@,,\n")); err == nil {
		t.Errorf("expected error for line with no versions")
	}
}

func TestParseTargets_RejectsMalformed(t *testing.T) {
	bad := []string{
		"no-at-sign-anywhere",
		"@scope-only-no-version",
		"trailing-at@",
	}
	for _, line := range bad {
		if _, _, err := parseTargets(strings.NewReader(line + "\n")); err == nil {
			t.Errorf("expected error for %q, got nil", line)
		}
	}
}

// pairSet is a small helper to compare unordered (name, version) lists.
func pairSet(pairs []nameVersionPair) map[nameVersionPair]bool {
	m := map[nameVersionPair]bool{}
	for _, p := range pairs {
		m[p] = true
	}
	return m
}

func TestParseNpmLock_V3(t *testing.T) {
	data := []byte(`{
      "name": "root", "version": "1.0.0", "lockfileVersion": 3,
      "packages": {
        "": {"name": "root", "version": "1.0.0"},
        "node_modules/chalk": {"version": "5.6.1"},
        "node_modules/foo/node_modules/chalk": {"version": "4.0.0"},
        "node_modules/@ctrl/tinycolor": {"version": "4.1.2"}
      }
    }`)
	want := map[nameVersionPair]bool{
		{"chalk", "5.6.1"}:           true,
		{"chalk", "4.0.0"}:           true,
		{"@ctrl/tinycolor", "4.1.2"}: true,
	}
	got := pairSet(parseNpmLock(data))
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for p := range want {
		if !got[p] {
			t.Errorf("missing pair %v", p)
		}
	}
}

func TestParseNpmLock_V1(t *testing.T) {
	data := []byte(`{
      "lockfileVersion": 1,
      "dependencies": {
        "chalk": {"version": "5.6.1"},
        "foo": {
          "version": "1.0.0",
          "dependencies": { "chalk": {"version": "4.0.0"} }
        }
      }
    }`)
	got := pairSet(parseNpmLock(data))
	want := []nameVersionPair{{"chalk", "5.6.1"}, {"foo", "1.0.0"}, {"chalk", "4.0.0"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("missing pair %v", p)
		}
	}
}

func TestParseYarnLock_Classic(t *testing.T) {
	data := []byte(`# yarn lockfile v1


chalk@^5.6.0, chalk@^5.6.1:
  version "5.6.1"
  resolved "https://registry.example/chalk-5.6.1.tgz"
  integrity sha512-AAA==

"@ctrl/tinycolor@^4.0.0":
  version "4.1.2"
  resolved "https://registry.example/tinycolor-4.1.2.tgz"
`)
	got := pairSet(parseYarnLock(data))
	want := []nameVersionPair{{"chalk", "5.6.1"}, {"@ctrl/tinycolor", "4.1.2"}}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("missing pair %v", p)
		}
	}
}

func TestParseYarnLock_Berry(t *testing.T) {
	data := []byte(`__metadata:
  version: 6

"chalk@npm:^5.6.0":
  version: 5.6.1
  resolution: "chalk@npm:5.6.1"
  checksum: abc
  languageName: node
  linkType: hard
`)
	got := parseYarnLock(data)
	if len(got) != 1 || got[0] != (nameVersionPair{"chalk", "5.6.1"}) {
		t.Errorf("got %v, want [{chalk 5.6.1}]", got)
	}
}

func TestParsePnpmLock(t *testing.T) {
	data := []byte(`lockfileVersion: '6.0'

packages:

  /chalk@5.6.1:
    resolution: {integrity: sha512-AAA==}
    engines: {node: '>=12'}

  /@ctrl/tinycolor@4.1.2:
    resolution: {integrity: sha512-BBB==}

  /react-dom@18.0.0(react@18.0.0):
    resolution: {integrity: sha512-CCC==}

  chalk@5.7.0:
    resolution: {integrity: sha512-DDD==}
`)
	got := pairSet(parsePnpmLock(data))
	want := []nameVersionPair{
		{"chalk", "5.6.1"},
		{"@ctrl/tinycolor", "4.1.2"},
		{"react-dom", "18.0.0"},
		{"chalk", "5.7.0"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for _, p := range want {
		if !got[p] {
			t.Errorf("missing pair %v", p)
		}
	}
}

func TestParseLockfile_DedupesAndDispatches(t *testing.T) {
	dir := t.TempDir()
	pl := dir + "/package-lock.json"
	// Same package appears in both packages and dependencies maps; dedup
	// should yield exactly one pair.
	body := `{
      "lockfileVersion": 3,
      "packages": { "node_modules/chalk": {"version": "5.6.1"} },
      "dependencies": { "chalk": {"version": "5.6.1"} }
    }`
	if err := writeFile(pl, body); err != nil {
		t.Fatal(err)
	}
	got := parseLockfile(pl)
	if len(got) != 1 || got[0] != (nameVersionPair{"chalk", "5.6.1"}) {
		t.Errorf("got %v, want one chalk@5.6.1", got)
	}
}

func writeFile(path, body string) error {
	return os.WriteFile(path, []byte(body), 0o644)
}
