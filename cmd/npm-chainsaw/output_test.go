package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestGroupHits_SortsAndDedupes(t *testing.T) {
	hits := []Hit{
		{Name: "chalk", Version: "5.6.1", Path: "/b/package.json", Kind: "package.json"},
		{Name: "chalk", Version: "5.6.1", Path: "/a/package.json", Kind: "package.json"},
		{Name: "chalk", Version: "5.6.1", Path: "/a/package.json", Kind: "package.json"}, // dup
		{Name: "chalk", Version: "5.7.0", Path: "/c/package.json", Kind: "package.json"},
		{Name: "@ctrl/tinycolor", Version: "4.1.2", Path: "/d/package.json", Kind: "package.json"},
	}
	groups := groupHits(hits)
	if len(groups) != 3 {
		t.Fatalf("got %d groups, want 3", len(groups))
	}
	// Sorted by name; "@ctrl/tinycolor" < "chalk".
	if groups[0].name != "@ctrl/tinycolor" || groups[1].name != "chalk" || groups[2].name != "chalk" {
		t.Errorf("unexpected group order: %+v", groups)
	}
	// chalk@5.6.1 should come before chalk@5.7.0.
	if groups[1].version != "5.6.1" || groups[2].version != "5.7.0" {
		t.Errorf("version sort wrong: %+v", groups)
	}
	// chalk@5.6.1 should have 2 unique locations (the duplicate folded).
	if len(groups[1].locations) != 2 {
		t.Errorf("expected 2 dedup'd locations, got %d", len(groups[1].locations))
	}
	if groups[1].locations[0].path != "/a/package.json" {
		t.Errorf("locations not sorted by path: %+v", groups[1].locations)
	}
}

func TestPrintHuman_Empty(t *testing.T) {
	targets := Targets{"chalk": {"5.6.1": true}, "lodash": {"*": true}}
	counts := Counts{PackageJSON: 1234, Lockfile: 56}
	var buf bytes.Buffer
	printHuman(&buf, nil, targets, counts, 250*time.Millisecond, false, false)
	out := buf.String()
	if !strings.Contains(out, "Scanned in 250ms:") {
		t.Errorf("missing footer header: %q", out)
	}
	// The footer table is whitespace-aligned; match loosely so column-width
	// tweaks don't break the test.
	if !regexp.MustCompile(`package\.json\s+1,234\s+files`).MatchString(out) {
		t.Errorf("missing package.json count row: %q", out)
	}
	if !regexp.MustCompile(`lockfiles\s+56\s+files`).MatchString(out) {
		t.Errorf("missing lockfiles count row: %q", out)
	}
	if !strings.Contains(out, "0 of 2 packages HIT") || !strings.Contains(out, "2 OK") {
		t.Errorf("missing summary line: %q", out)
	}
	// Default mode should NOT include the per-target Targets block.
	if strings.Contains(out, "ok   chalk@5.6.1") {
		t.Errorf("default mode should not show per-target block:\n%s", out)
	}
}

func TestPrintHuman_DefaultIsTerseHits(t *testing.T) {
	targets := Targets{
		"chalk":  {"5.6.1": true},
		"lodash": {"4.0.0": true},
	}
	hits := []Hit{
		{Name: "chalk", Version: "5.6.1", Path: "/x", Kind: "package.json"},
	}
	var buf bytes.Buffer
	printHuman(&buf, hits, targets, Counts{PackageJSON: 100}, time.Second, false, false)
	out := buf.String()
	if !strings.Contains(out, "HIT  chalk@5.6.1") {
		t.Errorf("expected HIT block for chalk:\n%s", out)
	}
	// Default mode: no per-target Targets block.
	if strings.Contains(out, "Targets:") || strings.Contains(out, "ok   lodash") {
		t.Errorf("default mode should not show Targets block:\n%s", out)
	}
	if !strings.Contains(out, "1 of 2 packages HIT") || !strings.Contains(out, "1 OK") {
		t.Errorf("wrong summary line:\n%s", out)
	}
}

func TestPrintHuman_VerboseAddsTargetsBlock(t *testing.T) {
	targets := Targets{
		"chalk":  {"5.6.1": true},
		"lodash": {"4.0.0": true},
	}
	hits := []Hit{
		{Name: "chalk", Version: "5.6.1", Path: "/x", Kind: "package.json"},
	}
	var buf bytes.Buffer
	printHuman(&buf, hits, targets, Counts{PackageJSON: 100}, time.Second, true, false)
	out := buf.String()
	if !strings.Contains(out, "Targets:") {
		t.Errorf("verbose mode missing Targets block:\n%s", out)
	}
	if !strings.Contains(out, "HIT  chalk@5.6.1  (1 location)") {
		t.Errorf("verbose missing per-target HIT row:\n%s", out)
	}
	if !strings.Contains(out, "ok   lodash@4.0.0") {
		t.Errorf("verbose missing per-target ok row:\n%s", out)
	}
}

func TestComputeTargetStatuses_WildcardMatches(t *testing.T) {
	targets := Targets{"chalk": {"*": true}}
	groups := []hitGroup{
		{name: "chalk", version: "5.6.1", locations: []locItem{{path: "/a"}, {path: "/b"}}},
	}
	rows := computeTargetStatuses(targets, groups)
	if len(rows) != 1 || rows[0].locations != 2 {
		t.Errorf("wildcard should count all matching versions, got %+v", rows)
	}
}

func TestPrintHuman_CapWithVerbose(t *testing.T) {
	// 25 hits, same package, distinct paths.
	var hits []Hit
	for i := 0; i < 25; i++ {
		hits = append(hits, Hit{
			Name: "chalk", Version: "5.6.1",
			Path: "/proj" + string(rune('a'+i)) + "/package.json",
			Kind: "package.json",
		})
	}
	targets := Targets{"chalk": {"5.6.1": true}}

	var compact bytes.Buffer
	printHuman(&compact, hits, targets, Counts{PackageJSON: 100}, time.Second, false, false)
	if !strings.Contains(compact.String(), "and 5 more") {
		t.Errorf("expected cap message in non-verbose output, got:\n%s", compact.String())
	}

	var verbose bytes.Buffer
	printHuman(&verbose, hits, targets, Counts{PackageJSON: 100}, time.Second, true, false)
	if strings.Contains(verbose.String(), "and 5 more") {
		t.Errorf("verbose should show all, but got cap message:\n%s", verbose.String())
	}
	// Verbose should mention all 25 paths.
	for i := 0; i < 25; i++ {
		needle := "/proj" + string(rune('a'+i)) + "/package.json"
		if !strings.Contains(verbose.String(), needle) {
			t.Errorf("verbose output missing %s", needle)
		}
	}
}

func TestPrintJSON_Shape(t *testing.T) {
	hits := []Hit{
		{Name: "chalk", Version: "5.6.1", Path: "/a", Kind: "package.json"},
		{Name: "chalk", Version: "5.6.1", Path: "/b", Kind: "lockfile"},
	}
	targets := Targets{
		"chalk":  {"5.6.1": true},
		"lodash": {"4.0.0": true},
	}
	counts := Counts{PackageJSON: 40, Lockfile: 2}
	var buf bytes.Buffer
	if err := printJSON(&buf, hits, targets, counts, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	var got struct {
		ScannedFiles int `json:"scanned_files"`
		ScanCounts   struct {
			PackageJSON int `json:"package_json"`
			Lockfile    int `json:"lockfile"`
			NpmCache    int `json:"npm_cache"`
		} `json:"scan_counts"`
		DurationMs int64 `json:"duration_ms"`
		Hits       []struct {
			Package, Version string
			Locations        []struct{ Path, Kind string }
		} `json:"hits"`
		Unmatched []struct{ Package, Version string } `json:"unmatched"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}
	if got.ScannedFiles != 42 || got.DurationMs != 100 {
		t.Errorf("wrong header fields: %+v", got)
	}
	if got.ScanCounts.PackageJSON != 40 || got.ScanCounts.Lockfile != 2 {
		t.Errorf("scan_counts wrong: %+v", got.ScanCounts)
	}
	if len(got.Hits) != 1 || got.Hits[0].Package != "chalk" || len(got.Hits[0].Locations) != 2 {
		t.Errorf("wrong hit grouping: %+v", got.Hits)
	}
	if len(got.Unmatched) != 1 || got.Unmatched[0].Package != "lodash" {
		t.Errorf("wrong unmatched list: %+v", got.Unmatched)
	}
}

func TestPrintJSON_EmptyArraysNotNull(t *testing.T) {
	var buf bytes.Buffer
	if err := printJSON(&buf, nil, Targets{}, Counts{}, 0); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.Contains(s, `"hits": []`) {
		t.Errorf("expected hits as empty array, got:\n%s", s)
	}
	if !strings.Contains(s, `"unmatched": []`) {
		t.Errorf("expected unmatched as empty array, got:\n%s", s)
	}
}

func TestShortenPath(t *testing.T) {
	cases := []struct{ in, home, want string }{
		{"/Users/elias/foo", "/Users/elias", "~/foo"},
		{"/Users/elias", "/Users/elias", "~"},
		{"/usr/local/lib", "/Users/elias", "/usr/local/lib"},
		{"/anything", "", "/anything"},
	}
	for _, c := range cases {
		if got := shortenPath(c.in, c.home); got != c.want {
			t.Errorf("shortenPath(%q, %q) = %q, want %q", c.in, c.home, got, c.want)
		}
	}
}

func TestCommafy(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1,000"},
		{12345, "12,345"},
		{1234567, "1,234,567"},
		{-12345, "-12,345"},
	}
	for _, c := range cases {
		if got := commafy(c.n); got != c.want {
			t.Errorf("commafy(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestDurStr(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{500 * time.Millisecond, "500ms"},
		{1500 * time.Millisecond, "1.5s"},
		{4123 * time.Millisecond, "4.1s"},
	}
	for _, c := range cases {
		if got := durStr(c.d); got != c.want {
			t.Errorf("durStr(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}
