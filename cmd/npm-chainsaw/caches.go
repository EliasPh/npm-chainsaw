package main

import (
	"bufio"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// scanCaches checks every known npm/pnpm/yarn cache and global install
// location for homeDir. Each location is best-effort: missing paths are
// silently skipped. homeDir is passed in (rather than calling
// os.UserHomeDir directly) so tests can target a synthetic home.
func scanCaches(homeDir string, targets Targets) []Hit {
	var hits []Hit

	// npm cache index only; never content-v2. The index is small and answers
	// "has this version ever been fetched on this machine".
	hits = append(hits,
		scanNpmCacheIndex(filepath.Join(homeDir, ".npm", "_cacache", "index-v5"), targets)...)

	// pnpm store: real installed packages, walk for package.json.
	for _, p := range pnpmStorePaths(homeDir) {
		hits = append(hits, scanForPackageJSONs(p, targets, "pnpm-store")...)
	}

	// Yarn Berry cache: parse zip filenames. Nothing is extracted.
	for _, p := range yarnBerryCachePaths(homeDir) {
		hits = append(hits, scanYarnBerryCache(p, targets)...)
	}

	// Yarn v1 cache: extracted folders, walk for package.json.
	for _, p := range yarnV1CachePaths(homeDir) {
		hits = append(hits, scanForPackageJSONs(p, targets, "yarn-cache")...)
	}

	// Global installs across the common Node version managers and system paths.
	for _, p := range globalNodeModulesPaths(homeDir) {
		hits = append(hits, scanForPackageJSONs(p, targets, "global")...)
	}
	return hits
}

// --- npm cache index --------------------------------------------------------

// npmTarballURLRE matches the registry URL fragment npm embeds in each cache
// index entry, e.g. "/chalk/-/chalk-5.6.1.tgz" or
// "/@ctrl/tinycolor/-/tinycolor-4.1.2.tgz". The "/-/" sentinel separates the
// package directory from the tarball filename.
var npmTarballURLRE = regexp.MustCompile(
	`/((?:@[^/]+/)?[^/]+)/-/[^/]+-([^/]+)\.tgz`)

// scanNpmCacheIndex walks ~/.npm/_cacache/index-v5/. Every file there is a
// "ledger": each line is "<integrity>\t<json>" for one cache entry. We pull
// name+version out of the tarball URL via regex, which is faster than
// json.Unmarshal and resilient to small format changes.
func scanNpmCacheIndex(root string, targets Targets) []Hit {
	var hits []Hit
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		// Cache entries can be long; bump the line ceiling.
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			m := npmTarballURLRE.FindStringSubmatch(scanner.Text())
			if m == nil {
				continue
			}
			if h, ok := matchNameVersion(m[1], m[2], path, "npm-cache", targets); ok {
				hits = append(hits, h)
			}
		}
		return nil
	})
	return hits
}

// matchNameVersion centralizes the target-lookup pattern so the cache
// scanners don't repeat the same three lines.
func matchNameVersion(name, version, path, kind string, targets Targets) (Hit, bool) {
	versions, ok := targets[name]
	if !ok {
		return Hit{}, false
	}
	if versions[version] || versions["*"] {
		return Hit{Name: name, Version: version, Path: path, Kind: kind}, true
	}
	return Hit{}, false
}

// --- generic package.json walk (pnpm store, yarn v1, globals) ---------------

// scanForPackageJSONs walks root looking for package.json files and matches
// each via matchPackageJSON with the given kind. Used wherever the cache
// layout is "real" installed packages. Only .git is skipped, since we're
// already inside a known cache and the broader skip rules don't apply.
func scanForPackageJSONs(root string, targets Targets, kind string) []Hit {
	var hits []Hit
	filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Name() != "package.json" {
			return nil
		}
		if h, ok := matchPackageJSON(path, targets, kind); ok {
			hits = append(hits, h)
		}
		return nil
	})
	return hits
}

// --- yarn berry cache (zip filenames only) ----------------------------------

// scanYarnBerryCache lists .zip files in the Berry cache and extracts
// (name, version) from each filename. Subdirectories are ignored; Berry
// stores everything flat at the cache root.
func scanYarnBerryCache(root string, targets Targets) []Hit {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var hits []Hit
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name, version, ok := nameFromYarnBerryFilename(e.Name())
		if !ok {
			continue
		}
		full := filepath.Join(root, e.Name())
		if h, ok := matchNameVersion(name, version, full, "yarn-cache", targets); ok {
			hits = append(hits, h)
		}
	}
	return hits
}

// berryProtocols are the markers Yarn Berry inserts between the package name
// and version in cache filenames. Order doesn't matter; we try each.
var berryProtocols = []string{
	"-npm-", "-workspace-", "-patch-", "-git-",
	"-file-", "-portal-", "-link-", "-virtual-", "-exec-",
}

// nameFromYarnBerryFilename parses Berry cache filenames such as
//
//	chalk-npm-5.6.1-deadbeef10-cf4c61a9bd.zip
//	@types-node-npm-18.0.0-aaaaaaaaaa-bbbbbbbbbb.zip
//
// The format is "<name>-<protocol>-<version>-<hashA>-<hashB>.zip" where the
// two trailing dash-segments are integrity hashes. For scoped packages,
// "@scope-name" in the filename is converted back to "@scope/name".
func nameFromYarnBerryFilename(base string) (string, string, bool) {
	if !strings.HasSuffix(base, ".zip") {
		return "", "", false
	}
	base = strings.TrimSuffix(base, ".zip")
	for _, proto := range berryProtocols {
		idx := strings.Index(base, proto)
		if idx < 0 {
			continue
		}
		rawName := base[:idx]
		rest := base[idx+len(proto):]
		// rest = "<version>-<hashA>-<hashB>". The last two dash-segments are
		// the integrity hashes; everything before is the version (which may
		// itself contain dashes, e.g. "1.0.0-beta.1").
		parts := strings.Split(rest, "-")
		if len(parts) < 3 {
			return "", "", false
		}
		version := strings.Join(parts[:len(parts)-2], "-")
		if version == "" || rawName == "" {
			return "", "", false
		}
		if strings.HasPrefix(rawName, "@") {
			// "@types-node" becomes "@types/node": replace the first "-" after "@".
			if i := strings.IndexByte(rawName[1:], '-'); i >= 0 {
				rawName = "@" + rawName[1:1+i] + "/" + rawName[1+i+1:]
			}
		}
		return rawName, version, true
	}
	return "", "", false
}

// --- platform paths ---------------------------------------------------------

// Each helper returns the conventional locations for one source. Paths that
// don't apply to the current platform are still returned; the scanners
// silently no-op on missing directories, so listing them is cheap.

func pnpmStorePaths(home string) []string {
	paths := []string{
		filepath.Join(home, "Library", "pnpm", "store", "v3"),     // macOS
		filepath.Join(home, ".local", "share", "pnpm", "store", "v3"), // Linux
	}
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("LOCALAPPDATA"); appdata != "" {
			paths = append(paths, filepath.Join(appdata, "pnpm", "store", "v3"))
		}
	}
	return paths
}

func yarnBerryCachePaths(home string) []string {
	return []string{filepath.Join(home, ".yarn", "berry", "cache")}
}

func yarnV1CachePaths(home string) []string {
	return []string{
		filepath.Join(home, "Library", "Caches", "Yarn"), // macOS
		filepath.Join(home, ".cache", "yarn"),            // Linux
	}
}

func globalNodeModulesPaths(home string) []string {
	paths := []string{
		"/usr/local/lib/node_modules",
		"/opt/homebrew/lib/node_modules",
		filepath.Join(home, ".npm-global", "lib", "node_modules"),
		filepath.Join(home, ".volta", "tools", "image", "packages"),
	}
	// nvm and fnm install per Node version; glob across them.
	paths = append(paths, globExpand(
		filepath.Join(home, ".nvm", "versions", "node", "*", "lib", "node_modules"))...)
	paths = append(paths, globExpand(
		filepath.Join(home, ".config", "fnm", "node-versions", "*", "installation", "lib", "node_modules"))...)
	if runtime.GOOS == "windows" {
		if appdata := os.Getenv("APPDATA"); appdata != "" {
			paths = append(paths, filepath.Join(appdata, "npm", "node_modules"))
		}
	}
	return paths
}

func globExpand(pattern string) []string {
	matches, _ := filepath.Glob(pattern)
	return matches
}
