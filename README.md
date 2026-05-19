# npm-chainsaw

A small CLI for scanning a machine for npm packages with known-bad versions.

When a supply chain attack against an npm package gets reported, drop the
affected `package@version` lines into a text file, run the binary, and you
get a report of what's currently installed and what was ever fetched on the
machine. Read-only, never touches anything on disk.

## Install

Download a macOS binary from the
[Releases page](https://github.com/EliasPh/npm-chainsaw/releases) and
`chmod +x` it. Or:

```sh
go install github.com/EliasPh/npm-chainsaw/cmd/npm-chainsaw@latest
```

Or clone the repo and run `go build ./cmd/npm-chainsaw`.

## Usage

```sh
npm-chainsaw incidents/some-list.txt              # scan $HOME (default)
npm-chainsaw incidents/some-list.txt ~/projects   # scan a specific path
npm-chainsaw list.txt --no-cache                  # skip npm/yarn/pnpm caches
npm-chainsaw list.txt --json                      # JSON output
npm-chainsaw list.txt --verbose                   # show all hit locations
```

Exit codes:
- `0` no hits
- `1` hits found
- `2` something went wrong

## List file format

Plain text. `#` starts a comment, blank lines are ignored. Each
non-comment line is `name@version`, with these options:

- Multiple versions of the same package: comma-separated (whitespace
  around the commas is fine)
- `name@*` matches any version (use this when a maintainer was fully
  compromised and no version of the package can be trusted)

```
# 2025-10 example
# source: https://example.invalid/
@ctrl/tinycolor@4.1.2
chalk@5.6.1
wot-api@0.8.1, 0.8.2, 0.8.3, 0.8.4
suspicious-pkg@*
```

See [`incidents/TEMPLATE.txt`](incidents/TEMPLATE.txt) for a copy-paste
starting point. New incident files welcome via PR, see
[`incidents/README.md`](incidents/README.md).

## What gets scanned

- `package.json` files anywhere under the scan root, including deeply
  nested `node_modules`.
- Lockfiles: `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`,
  `pnpm-lock.yaml`.
- Package manager caches: npm cache **index** (not the multi-GB tarball
  store), pnpm store, Yarn Berry and v1 caches.
- Common global install paths (Homebrew, nvm, fnm, Volta, system,
  Windows AppData).

## Limitations

- Exact versions only, plus `@*`. No semver ranges.
- Cache scanning is best-effort across many package manager versions;
  the `package.json` walk is the source of truth.
- No git history, no advisory fetching, no remediation.

### Why exact versions and not semver ranges?

Reports publish exact bad versions, not ranges. If `chalk@5.6.1` is
compromised, then `5.6.2` is probably the fix release and is safe.
Flagging it would be a false positive that makes every other hit harder
to trust.

Note that `^5.6.1` in `package.json` is a range, but `npm install`
resolves each range to a specific exact version, and that's what ends
up on disk and in the lockfile. This scanner reads what's installed,
not what was declared. So `chalk@5.6.1` in the list matches only
installs that resolved to exactly `5.6.1`.

For the "no version of the package is safe" case, use the `@*` wildcard.

## Why a binary instead of `npm audit`?

`npm audit` runs per-project. This runs once across the whole machine
and checks caches too.

## macOS Gatekeeper

Binaries from the Releases page are unsigned. If macOS blocks one on first
run, clear the quarantine flag or do the right-click dance:

```sh
xattr -d com.apple.quarantine npm-chainsaw
```

## License

MIT
