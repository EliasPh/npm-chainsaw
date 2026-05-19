package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"
)

// defaultLocationCap is the per-package location limit in human output.
// Beyond this we show a "and N more" line; --verbose shows everything.
const defaultLocationCap = 20

// ansi holds optional ANSI escape codes. Empty strings when color is off,
// so callers can interpolate them without conditionals.
type ansi struct {
	bold, red, green, dim, reset string
}

func ansiCodes(enabled bool) ansi {
	if !enabled {
		return ansi{}
	}
	return ansi{
		bold:  "\x1b[1m",
		red:   "\x1b[31m",
		green: "\x1b[32m",
		dim:   "\x1b[2m",
		reset: "\x1b[0m",
	}
}

// colorEnabled reports whether we should emit ANSI escapes. Respects the
// NO_COLOR convention and only enables color when stdout is a terminal.
func colorEnabled(f *os.File) bool {
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// locItem and hitGroup are the shaped-for-output forms used by both human
// and JSON renderers.
type locItem struct{ path, kind string }
type hitGroup struct {
	name, version string
	locations     []locItem
}

// groupHits sorts and groups hits by name+version. Within a group, the same
// (path, kind) is collapsed to a single entry; the parallel scan can in
// theory hand us duplicates if the same file is reached via two routes.
func groupHits(hits []Hit) []hitGroup {
	sorted := make([]Hit, len(hits))
	copy(sorted, hits)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		if sorted[i].Version != sorted[j].Version {
			return sorted[i].Version < sorted[j].Version
		}
		return sorted[i].Path < sorted[j].Path
	})

	var groups []hitGroup
	var cur *hitGroup
	seen := map[string]bool{}
	for _, h := range sorted {
		if cur == nil || cur.name != h.Name || cur.version != h.Version {
			groups = append(groups, hitGroup{name: h.Name, version: h.Version})
			cur = &groups[len(groups)-1]
			seen = map[string]bool{} // dedupe scope is per group
		}
		key := h.Path + "|" + h.Kind
		if seen[key] {
			continue
		}
		seen[key] = true
		cur.locations = append(cur.locations, locItem{path: h.Path, kind: h.Kind})
	}
	return groups
}

// printHuman writes a grouped, human-friendly report.
//
// In default mode the output is intentionally terse: HIT blocks (capped at
// 20 locations each) and a per-kind breakdown footer. --verbose adds the
// full per-target HIT/ok block at the bottom and removes the location cap.
func printHuman(w io.Writer, hits []Hit, targets Targets, counts Counts, dur time.Duration, verbose, color bool) {
	a := ansiCodes(color)
	home, _ := os.UserHomeDir()
	groups := groupHits(hits)

	totalLocs := 0
	for _, g := range groups {
		totalLocs += len(g.locations)
		fmt.Fprintf(w, "%s%sHIT%s  %s%s@%s%s\n",
			a.bold, a.red, a.reset, a.bold, g.name, g.version, a.reset)
		shown := g.locations
		if !verbose && len(shown) > defaultLocationCap {
			shown = shown[:defaultLocationCap]
		}
		for _, loc := range shown {
			fmt.Fprintf(w, "     %s%s\n", shortenPath(loc.path, home), kindAnnotation(loc.kind, a))
		}
		if !verbose && len(g.locations) > defaultLocationCap {
			extra := len(g.locations) - defaultLocationCap
			fmt.Fprintf(w, "     %s... and %d more (use --verbose to see all)%s\n",
				a.dim, extra, a.reset)
		}
		fmt.Fprintln(w)
	}

	// Per-specifier HIT/ok rows: verbose only. The default mode keeps the
	// report short for incidents with hundreds of targets.
	if verbose {
		statuses := computeTargetStatuses(targets, groups)
		fmt.Fprintln(w, "Targets:")
		for _, s := range statuses {
			if s.locations > 0 {
				fmt.Fprintf(w, "  %s%sHIT%s  %s@%s  %s(%d %s)%s\n",
					a.bold, a.red, a.reset, s.name, s.version,
					a.dim, s.locations, plural(s.locations, "location", "locations"), a.reset)
			} else {
				fmt.Fprintf(w, "  %s%sok%s   %s@%s\n",
					a.green, a.dim, a.reset, s.name, s.version)
			}
		}
		fmt.Fprintln(w)
	}

	// Per-kind breakdown. Reassuring confirmation that the scan did the
	// work, even when there are zero hits.
	fmt.Fprintf(w, "Scanned in %s:\n", durStr(dur))
	for _, row := range countRows(counts) {
		fmt.Fprintf(w, "  %-12s %8s %s\n", row.label, commafy(row.n), row.unit)
	}
	fmt.Fprintln(w)

	// Summary: how many packages hit vs were checked.
	hitN := len(groups)
	total := len(targets)
	if hitN == 0 {
		fmt.Fprintf(w, "%s%d of %d packages HIT%s, %s%d OK%s\n",
			a.dim, hitN, total, a.reset,
			a.green, total, a.reset)
	} else {
		fmt.Fprintf(w, "%s%s%d of %d packages HIT%s, %s%d OK%s\n",
			a.bold, a.red, hitN, total, a.reset,
			a.green, total-hitN, a.reset)
	}
}

// countRow is one line of the per-kind footer table.
type countRow struct {
	label string
	n     int
	unit  string
}

// countRows returns the breakdown rows in display order.
func countRows(c Counts) []countRow {
	return []countRow{
		{"package.json", c.PackageJSON, "files"},
		{"lockfiles", c.Lockfile, "files"},
		{"npm cache", c.NpmCache, "entries"},
		{"pnpm store", c.PnpmStore, "files"},
		{"yarn cache", c.YarnCache, "files"},
		{"global", c.Global, "files"},
	}
}

// printJSON writes the machine-readable form. Paths are absolute (no ~
// substitution; that's only for human display).
func printJSON(w io.Writer, hits []Hit, targets Targets, counts Counts, dur time.Duration) error {
	type jsonLoc struct {
		Path string `json:"path"`
		Kind string `json:"kind"`
	}
	type jsonGroup struct {
		Package   string    `json:"package"`
		Version   string    `json:"version"`
		Locations []jsonLoc `json:"locations"`
	}
	type jsonUnmatched struct {
		Package string `json:"package"`
		Version string `json:"version"`
	}
	type jsonCounts struct {
		PackageJSON int `json:"package_json"`
		Lockfile    int `json:"lockfile"`
		NpmCache    int `json:"npm_cache"`
		PnpmStore   int `json:"pnpm_store"`
		YarnCache   int `json:"yarn_cache"`
		Global      int `json:"global"`
	}
	out := struct {
		ScannedFiles int             `json:"scanned_files"`
		ScanCounts   jsonCounts      `json:"scan_counts"`
		DurationMs   int64           `json:"duration_ms"`
		Hits         []jsonGroup     `json:"hits"`
		Unmatched    []jsonUnmatched `json:"unmatched"`
	}{
		ScannedFiles: counts.Total(),
		ScanCounts: jsonCounts{
			PackageJSON: counts.PackageJSON,
			Lockfile:    counts.Lockfile,
			NpmCache:    counts.NpmCache,
			PnpmStore:   counts.PnpmStore,
			YarnCache:   counts.YarnCache,
			Global:      counts.Global,
		},
		DurationMs: dur.Milliseconds(),
		Hits:       []jsonGroup{},     // emit "[]" not "null" when empty
		Unmatched:  []jsonUnmatched{}, // ditto
	}
	groups := groupHits(hits)
	for _, g := range groups {
		jg := jsonGroup{Package: g.name, Version: g.version}
		for _, l := range g.locations {
			jg.Locations = append(jg.Locations, jsonLoc{Path: l.path, Kind: l.kind})
		}
		out.Hits = append(out.Hits, jg)
	}
	for _, s := range computeTargetStatuses(targets, groups) {
		if s.locations == 0 {
			out.Unmatched = append(out.Unmatched, jsonUnmatched{Package: s.name, Version: s.version})
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// targetStatus is one row in the per-specifier results block.
type targetStatus struct {
	name, version string
	locations     int
}

// computeTargetStatuses returns one row per specifier in targets, annotated
// with the count of matching hit locations. Wildcard "@*" entries are
// matched when ANY version of that package appears in hits.
func computeTargetStatuses(targets Targets, groups []hitGroup) []targetStatus {
	// Build name -> version -> location count from the (already deduped) groups.
	matched := map[string]map[string]int{}
	for _, g := range groups {
		if matched[g.name] == nil {
			matched[g.name] = map[string]int{}
		}
		matched[g.name][g.version] = len(g.locations)
	}
	var rows []targetStatus
	for name, versions := range targets {
		for v := range versions {
			row := targetStatus{name: name, version: v}
			if v == "*" {
				// Wildcard matches any version; sum locations across all.
				for _, c := range matched[name] {
					row.locations += c
				}
			} else {
				row.locations = matched[name][v]
			}
			rows = append(rows, row)
		}
	}
	// HITs first, then alphabetical by name+version.
	sort.Slice(rows, func(i, j int) bool {
		ihit := rows[i].locations > 0
		jhit := rows[j].locations > 0
		if ihit != jhit {
			return ihit
		}
		if rows[i].name != rows[j].name {
			return rows[i].name < rows[j].name
		}
		return rows[i].version < rows[j].version
	})
	return rows
}

// kindAnnotation maps a Hit.Kind to its parenthesized display label. The
// most common case ("package.json") is unlabeled to keep the output quiet.
func kindAnnotation(kind string, a ansi) string {
	label := ""
	switch kind {
	case "lockfile":
		label = "(lockfile)"
	case "npm-cache":
		label = "(npm cache)"
	case "yarn-cache":
		label = "(yarn cache)"
	case "pnpm-store":
		label = "(pnpm store)"
	case "global":
		label = "(global install)"
	default:
		return ""
	}
	return "  " + a.dim + label + a.reset
}

// shortenPath replaces a leading $HOME with "~" for compactness in human
// output. Leaves the path unchanged when home is empty or doesn't match.
func shortenPath(p, home string) string {
	if home != "" && strings.HasPrefix(p, home) {
		return "~" + p[len(home):]
	}
	return p
}

// commafy adds thousand separators to an integer.
func commafy(n int) string {
	s := fmt.Sprint(n)
	if n < 1000 && n > -1000 {
		return s
	}
	// Handle negatives by treating the digit portion separately.
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		if len(s) > pre {
			b.WriteByte(',')
		}
	}
	for i := pre; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	return sign + b.String()
}

// printSearchHeader writes the "what we're checking" preamble.
//
// Default mode is a single line ("Checking N packages..."). Verbose mode
// expands to the full target list, one per package, with versions joined
// by commas (matching the incident-list input format). Caller suppresses
// it entirely under --json.
func printSearchHeader(w io.Writer, listPath string, targets Targets, specifiers int, verbose bool) {
	from := ""
	if listPath != "" {
		from = " from " + listPath
	}
	fmt.Fprintf(w, "Checking %d %s (%d %s)%s\n",
		len(targets), plural(len(targets), "package", "packages"),
		specifiers, plural(specifiers, "specifier", "specifiers"),
		from)
	if !verbose {
		return
	}
	names := make([]string, 0, len(targets))
	for n := range targets {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		versions := make([]string, 0, len(targets[n]))
		for v := range targets[n] {
			versions = append(versions, v)
		}
		sort.Strings(versions)
		fmt.Fprintf(w, "  %s@%s\n", n, strings.Join(versions, ", "))
	}
	fmt.Fprintln(w)
}

// shouldShowProgress reports whether to display the live "scanned N..." line.
// Suppressed under --json (don't pollute machine output) and when stderr is
// not a terminal (the \r-overwrite trick looks ugly in log files).
func shouldShowProgress(jsonMode bool) bool {
	if jsonMode {
		return false
	}
	fi, err := os.Stderr.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// progressLoop prints "scanned N files..." to stderr roughly every 500ms,
// starting only after a 2s warm-up so short scans stay quiet. Stops when
// done is closed and clears the line so it doesn't sit above the report.
func progressLoop(counter *atomic.Int64, done <-chan struct{}) {
	// Warm-up delay: skip progress entirely for quick scans.
	select {
	case <-done:
		return
	case <-time.After(2 * time.Second):
	}
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		fmt.Fprintf(os.Stderr, "\rscanned %s files...", commafy(int(counter.Load())))
		select {
		case <-done:
			// \r + ANSI clear-line so the final report doesn't share space.
			fmt.Fprint(os.Stderr, "\r\x1b[2K")
			return
		case <-ticker.C:
		}
	}
}

// plural picks the singular or plural noun based on count.
func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// durStr formats a duration as "230ms" or "4.1s".
func durStr(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return fmt.Sprintf("%.1fs", d.Seconds())
}
