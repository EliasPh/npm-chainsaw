package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Targets is the lookup built from an incident list.
//
// Outer key: package name (e.g. "chalk" or "@ctrl/tinycolor").
// Inner key: version string, or "*" to mean "any version".
type Targets map[string]map[string]bool

// parseTargets reads an incident list and returns the lookup map plus the
// number of unique name@version specifiers loaded.
//
// Format (see incidents/README.md):
//   - one "name@version" or "name@*" per line
//   - "#" starts a comment; blank lines and comment lines are skipped
//   - scoped packages: the LAST "@" separates name from version,
//     because scope names themselves begin with "@"
//   - multiple versions per line: comma-separated after the "@",
//     e.g. "wot-api@0.8.1, 0.8.2, 0.8.3". Whitespace around commas is
//     tolerated; empty entries (e.g. trailing commas) are skipped.
func parseTargets(r io.Reader) (Targets, int, error) {
	t := Targets{}
	count := 0
	scanner := bufio.NewScanner(r)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Last "@" splits name from version(s). For "@scope/name@1.0.0" this
		// lands on the second "@"; for "foo@1.0.0" on the only one.
		idx := strings.LastIndex(line, "@")
		if idx <= 0 {
			return nil, 0, fmt.Errorf("line %d: %q is not name@version", lineNo, line)
		}
		name := line[:idx]
		versionList := line[idx+1:]
		if versionList == "" {
			return nil, 0, fmt.Errorf("line %d: %q has empty version", lineNo, line)
		}
		added := 0
		for _, v := range strings.Split(versionList, ",") {
			v = strings.TrimSpace(v)
			if v == "" {
				continue // paste-friendly: silently drop stray commas
			}
			if t[name] == nil {
				t[name] = map[string]bool{}
			}
			if !t[name][v] {
				t[name][v] = true
				count++
			}
			added++
		}
		if added == 0 {
			return nil, 0, fmt.Errorf("line %d: %q has no versions", lineNo, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("reading incident list: %w", err)
	}
	return t, count, nil
}

// ----------------------------------------------------------------------------
// Lockfile parsing
//
// Each parser is pure: bytes in, (name, version) pairs out. Errors are
// swallowed; the package.json walk is the ground truth, and a lockfile we
// can't parse cleanly is better treated as "no extra evidence" than as a
// crash. Limitations are documented per parser.
// ----------------------------------------------------------------------------

// nameVersionPair is one extracted (name, version) tuple from a lockfile.
type nameVersionPair struct {
	name, version string
}

// parseLockfile reads the file at path, dispatches to the right parser by
// filename, and dedupes pairs within a single lockfile.
func parseLockfile(path string) []nameVersionPair {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var pairs []nameVersionPair
	switch filepath.Base(path) {
	case "package-lock.json", "npm-shrinkwrap.json":
		pairs = parseNpmLock(data)
	case "yarn.lock":
		pairs = parseYarnLock(data)
	case "pnpm-lock.yaml":
		pairs = parsePnpmLock(data)
	}
	return dedupePairs(pairs)
}

func dedupePairs(in []nameVersionPair) []nameVersionPair {
	seen := make(map[nameVersionPair]bool, len(in))
	out := in[:0]
	for _, p := range in {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

// --- npm lockfiles (package-lock.json, npm-shrinkwrap.json) -----------------

// parseNpmLock handles lockfileVersion 1, 2, and 3.
//
// v2/v3 use a flat "packages" map keyed by paths like
//
//	"node_modules/foo/node_modules/chalk"
//
// where the name is the segment after the LAST "node_modules/". Some entries
// carry an explicit "name" field (e.g. aliased packages); that wins.
//
// v1 uses a nested "dependencies" tree keyed by the package name directly.
// Both forms can coexist; we collect from both.
func parseNpmLock(data []byte) []nameVersionPair {
	var raw struct {
		Packages map[string]struct {
			Name    string `json:"name,omitempty"`
			Version string `json:"version"`
		} `json:"packages"`
		Dependencies map[string]*npmLockV1Dep `json:"dependencies"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil
	}
	var out []nameVersionPair
	for key, entry := range raw.Packages {
		if key == "" || entry.Version == "" {
			continue // "" is the root project itself
		}
		name := entry.Name
		if name == "" {
			name = nameFromLockKey(key)
		}
		if name != "" {
			out = append(out, nameVersionPair{name, entry.Version})
		}
	}
	walkV1Deps(raw.Dependencies, &out)
	return out
}

// npmLockV1Dep is the recursive shape used by lockfileVersion 1.
type npmLockV1Dep struct {
	Version      string                   `json:"version,omitempty"`
	Dependencies map[string]*npmLockV1Dep `json:"dependencies,omitempty"`
}

func walkV1Deps(deps map[string]*npmLockV1Dep, out *[]nameVersionPair) {
	for name, d := range deps {
		if d == nil {
			continue
		}
		if d.Version != "" {
			*out = append(*out, nameVersionPair{name, d.Version})
		}
		walkV1Deps(d.Dependencies, out)
	}
}

// nameFromLockKey extracts the package name from a v2/v3 "packages" map key.
// Returns "" if the key isn't in the expected node_modules/<name> form.
func nameFromLockKey(key string) string {
	const sep = "node_modules/"
	idx := strings.LastIndex(key, sep)
	if idx < 0 {
		return ""
	}
	return key[idx+len(sep):]
}

// --- yarn.lock (classic v1 + Berry v2+) -------------------------------------

// parseYarnLock walks yarn.lock as paragraphs of "header\n  fields...".
// Headers list one or more "name@constraint" specs (comma-separated, possibly
// quoted). The version field below the header (either `version "x"` for
// classic or `version: x` for Berry) gives the resolved version, which we
// emit once per name in the header.
//
// Known limitations:
//   - aliased packages like "foo@npm:bar@^1.0.0" extract "foo@npm:bar" as
//     the name; rare in practice and not worth a fuller parser
func parseYarnLock(data []byte) []nameVersionPair {
	var out []nameVersionPair
	var currentNames []string
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		raw := scanner.Text()
		trimmed := strings.TrimSpace(raw)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			currentNames = currentNames[:0]
			continue
		}
		isIndented := raw != "" && (raw[0] == ' ' || raw[0] == '\t')
		if !isIndented {
			// New header. Reset state and parse name(s) from "spec, spec, ...:"
			currentNames = currentNames[:0]
			if !strings.HasSuffix(trimmed, ":") {
				continue
			}
			head := strings.TrimSuffix(trimmed, ":")
			if !strings.Contains(head, "@") {
				continue // pseudo-blocks like "__metadata:"
			}
			for _, spec := range strings.Split(head, ",") {
				spec = strings.TrimSpace(spec)
				spec = strings.Trim(spec, `"`)
				if name := nameFromYarnSpec(spec); name != "" {
					currentNames = append(currentNames, name)
				}
			}
			continue
		}
		// Indented line under a header: look for a version field.
		if len(currentNames) == 0 {
			continue
		}
		if v := extractYarnVersion(trimmed); v != "" {
			for _, n := range currentNames {
				out = append(out, nameVersionPair{n, v})
			}
			currentNames = currentNames[:0]
		}
	}
	return out
}

// nameFromYarnSpec returns the package name from a yarn spec like
// "chalk@^5.6.0" or "@scope/name@npm:^4.0.0". The LAST "@" (not at index 0,
// which would be the scope marker) separates the name from the constraint.
func nameFromYarnSpec(spec string) string {
	idx := strings.LastIndex(spec, "@")
	if idx <= 0 {
		return spec
	}
	return spec[:idx]
}

// extractYarnVersion pulls the version out of a `version "x"` (classic) or
// `version: x` (Berry) line, returning "" if the line is something else.
func extractYarnVersion(line string) string {
	if !strings.HasPrefix(line, "version") {
		return ""
	}
	rest := strings.TrimPrefix(line, "version")
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, ":")
	rest = strings.TrimSpace(rest)
	rest = strings.Trim(rest, `"`)
	if i := strings.IndexAny(rest, " \t"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// --- pnpm-lock.yaml ---------------------------------------------------------

// pnpmPkgRE matches the package-key lines in a pnpm lockfile, e.g.
//
//	  /chalk@5.6.1:
//	  /@ctrl/tinycolor@4.1.2:
//	  /react-dom@18.0.0(react@18.0.0):
//	  chalk@5.6.1:                       (v9 dropped the leading slash)
//
// The optional "(peer@ver)" suffix is consumed but discarded.
//
// Known limitation: the pre-v6 form `/name/version:` (slash separator) is
// not supported; if the upgrade to "@"-separated keys happened before this
// tool was useful, those projects are likely already upgraded.
var pnpmPkgRE = regexp.MustCompile(
	`^\s+'?/?((?:@[^@/\s'"()]+/)?[^@/\s'"()]+)@([^@\s'"():]+)(?:\([^)]*\))*'?:`)

func parsePnpmLock(data []byte) []nameVersionPair {
	var out []nameVersionPair
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		m := pnpmPkgRE.FindStringSubmatch(scanner.Text())
		if m == nil {
			continue
		}
		out = append(out, nameVersionPair{m[1], m[2]})
	}
	return out
}
