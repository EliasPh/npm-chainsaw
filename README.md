<p align="center">
  <img src="assets/logo.png" alt="npm-chainsaw" width="320">
</p>

# npm-chainsaw

[![Test](https://github.com/EliasPh/npm-chainsaw/actions/workflows/test.yml/badge.svg)](https://github.com/EliasPh/npm-chainsaw/actions/workflows/test.yml)

Scan a machine for npm packages with known-bad versions.

Drop the affected `package@version` lines into a text file and run the binary.
You get a report of what's installed and what was ever fetched on the machine.

## Read-only

npm-chainsaw only opens files for reading. It never writes, deletes, renames,
changes permissions, or runs other programs.

This is enforced, not just claimed: [`readonly_test.go`](cmd/npm-chainsaw/readonly_test.go)
parses the source and fails the build if a write call (`os.WriteFile`,
`os.Remove`, `os.OpenFile`, `os/exec`, and friends) reaches the scanner, and CI
runs it on every push. It's a denylist, not a sandbox, so it isn't proof against
deliberately obscure tricks.

## Install

From source (recommended):

```sh
go install github.com/EliasPh/npm-chainsaw/cmd/npm-chainsaw@latest
```

Or clone the repo and run `go build ./cmd/npm-chainsaw`.

Prebuilt macOS and Linux binaries are on the
[Releases page](https://github.com/EliasPh/npm-chainsaw/releases). They are
unsigned; verify a download against the release's `SHA256SUMS`:

```sh
shasum -a 256 -c SHA256SUMS    # run in the dir holding the downloaded binary
```

macOS blocks unsigned binaries on first run. Build from source to avoid this, or
clear the quarantine flag:

```sh
xattr -d com.apple.quarantine npm-chainsaw
```

## Usage

```sh
npm-chainsaw examples/example-list.txt              # scan $HOME (default)
npm-chainsaw examples/example-list.txt ~/projects   # scan a specific path
npm-chainsaw list.txt --no-cache                    # walk the path only; skip caches & global dirs
npm-chainsaw list.txt --json                        # JSON output
npm-chainsaw list.txt --verbose                     # show all hit locations + gaps
```

[`examples/example-list.txt`](examples/example-list.txt) is a non-real list to
try the tool with; use one from [`incidents/`](incidents/) for real scans.

Exit codes:

- `0` no hits, scan complete
- `1` hits found
- `2` error
- `3` no hits, but the scan was incomplete (a location that could hide a package
  couldn't be read)

A clean machine is `exit 0`, so CI can gate on it.

`complete` / `exit 0` means nothing the scan read contained a hit. It is not a
claim about paths outside the scan root, or about the caches when `--no-cache`
is set. The verdict line names the root it read; pass `/` for a machine-wide
scan.

## Incident lists

Plain text, one `name@version` per line. `#` starts a comment; blank lines are
ignored.

- Comma-separated for multiple versions: `name@1.2.3, 1.2.4`
- `name@*` matches any version (for a fully compromised package)

```
# source: https://example.invalid/
@ctrl/tinycolor@4.1.2
chalk@5.6.1
wot-api@0.8.1, 0.8.2, 0.8.3, 0.8.4
suspicious-pkg@*
```

Browse [`incidents/`](incidents/), or grab one directly:

```sh
curl -O https://raw.githubusercontent.com/EliasPh/npm-chainsaw/main/incidents/<file>.txt
```

See [`incidents/TEMPLATE.txt`](incidents/TEMPLATE.txt) for the format and
[`incidents/README.md`](incidents/README.md) to contribute a list.

## What it scans

It walks the scan root and reads every `package.json` and lockfile under it,
including hidden directories (`~/.config`, `~/.vscode`, `~/.nvm`, and the like).
Two directories are skipped: `.git`, and the npm tarball store
(`_cacache/content-v2`); what was fetched there is read from the npm cache index
instead.

On top of the walk it checks:

- Lockfiles: `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`,
  `pnpm-lock.yaml`
- The npm cache index and the Yarn Berry cache (what was ever fetched)
- Global installs outside `$HOME` (Homebrew, system, Windows AppData) and the
  per-version dirs for nvm, fnm, and Volta

Anything already under the scan root isn't counted twice.

## What it does not do

- No semver ranges. Exact versions only, plus `@*`.
- Doesn't follow symlinks: a `node_modules` reachable only through a symlink
  pointing outside the scan root isn't walked.
- The default root is `$HOME` plus the global dirs. Pass `/` for a whole machine
  (slower, may need `sudo`).
- Cache and Berry parsing is best-effort across package-manager versions; the
  `package.json` walk is the source of truth.
- No git history, no network fetching, no remediation.

## License

MIT
