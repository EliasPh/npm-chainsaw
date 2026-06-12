<p align="center">
  <img src="assets/logo.png" alt="npm-chainsaw" width="320">
</p>

# npm-chainsaw

[![Test](https://github.com/EliasPh/npm-chainsaw/actions/workflows/test.yml/badge.svg)](https://github.com/EliasPh/npm-chainsaw/actions/workflows/test.yml)

A small CLI for scanning a machine for npm packages with known-bad versions.

When a supply chain attack against an npm package gets reported, drop the
affected `package@version` lines into a text file, run the binary, and you
get a report of what's currently installed and what was ever fetched on the
machine.

## Read-only

npm-chainsaw only opens files for reading. It never writes, deletes, renames,
changes permissions, or runs other programs. Nothing to undo after a scan.

This is checked, not just claimed. A test
([`readonly_test.go`](cmd/npm-chainsaw/readonly_test.go)) parses the source and
fails if one of the common write calls (`os.WriteFile`, `os.Remove`,
`os.OpenFile`, `os/exec`, and friends) lands in the scanner. CI runs the test
suite on every push, so a change that adds one can't be merged. The only writes
in the repo are in the tests, and those stay in temp directories.

It's a denylist of the calls that mutate the filesystem or run programs, not a
sandbox, so it isn't bulletproof against deliberately obscure tricks. But it
catches the realistic ways a write would creep in, and the scanner has none.

## Install

**From source (recommended).** Builds the binary yourself, so there's no
unsigned-download trust step and no Gatekeeper prompt:

```sh
go install github.com/EliasPh/npm-chainsaw/cmd/npm-chainsaw@latest
```

Or clone the repo and run `go build ./cmd/npm-chainsaw`.

**Prebuilt binary.** Download a macOS or Linux binary from the
[Releases page](https://github.com/EliasPh/npm-chainsaw/releases) and `chmod +x`
it. Each release also publishes a `SHA256SUMS` file — verify before running:

```sh
shasum -a 256 -c SHA256SUMS    # run in the dir holding the downloaded binary
```

The prebuilt macOS binaries are **unsigned**. macOS will block an unsigned
binary on first run; the safe way past that is to build from source (above). If
you understand the trade-off and still want to run the download, you can clear
the quarantine flag manually:

```sh
xattr -d com.apple.quarantine npm-chainsaw
```

## Usage

```sh
npm-chainsaw examples/example-list.txt              # scan $HOME (default)
npm-chainsaw examples/example-list.txt ~/projects   # scan a specific path
npm-chainsaw list.txt --no-cache                    # only walk the path; skip caches & global dirs
npm-chainsaw list.txt --json                        # JSON output
npm-chainsaw list.txt --verbose                     # show all hit locations + gaps
```

[`examples/example-list.txt`](examples/example-list.txt) is a small,
non-real list to try the tool with; swap it for one from
[`incidents/`](incidents/) when scanning for real.

Exit codes: `0` no hits and scan complete, `1` hits found, `2` something
went wrong, `3` no hits but the scan was **incomplete** (a location that
could hide an affected package couldn't be read — see Completeness below).
A clean machine is `exit 0`, so CI can gate on it exactly.

**What `complete` / `exit 0` means.** It says: *nothing the scan was configured
to read contained a hit.* It is **not** a claim about anything outside the scan
root (other users' homes, system paths you didn't pass), and with `--no-cache`
it does not cover the package manager caches. The verdict line names the root it
actually read so this scope is always visible. Widen the root (e.g. pass `/`) if
you need a machine-wide answer.

## Incident lists

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

Browse [`incidents/`](incidents/) for known lists, or grab one directly:

```sh
curl -O https://raw.githubusercontent.com/EliasPh/npm-chainsaw/main/incidents/<file>.txt
```

See [`incidents/TEMPLATE.txt`](incidents/TEMPLATE.txt) for a copy-paste
starting point and [`incidents/README.md`](incidents/README.md) for how to
contribute a new list via PR.

## How it works

The core of the scan is exhaustive on purpose: it walks the root and reads
**every `package.json` and lockfile** under it. That includes hidden
directories like `~/.config`, `~/.vscode`, and `~/.nvm` — editor extensions
and version managers ship real `node_modules`, and a security scan that
skipped them would miss installs. Only two kinds of directory are skipped,
and only because they provably contain no installed package's
`package.json`:

- `.git` directories (version-control internals, never an install), and
- the npm tarball store (`_cacache/content-v2`): thousands of compressed
  blobs with no readable `package.json`. What was ever *fetched* there is
  read instead from the small npm cache index.

On top of that walk, it also checks:

- Lockfiles: `package-lock.json`, `npm-shrinkwrap.json`, `yarn.lock`,
  `pnpm-lock.yaml`.
- The npm cache **index** (what was ever fetched) and the Yarn Berry cache.
- Global install paths *outside* your home directory (Homebrew, system,
  Windows AppData) and the per-version dirs for nvm, fnm, and Volta.

Anything in that second list that happens to live under the scan root is
already covered by the walk, so it's never reported or counted twice. A
completeness test (`scanner_test.go`) plants `package.json` files in hidden,
nested, and awkward locations and fails if any is missed.

**Limitations:**

- Exact versions only, plus `@*`. No semver ranges.
- Symlinks are not followed, so a `node_modules` reachable only through a
  symlink pointing outside the scan root won't be walked.
- The default scan root is `$HOME` plus the system-wide global dirs. To
  cover an entire machine (other users, system paths), pass `/` explicitly —
  it's slower and may need `sudo` to read files you don't own.
- Cache index/Berry parsing is best-effort across package-manager versions;
  the `package.json` walk is the source of truth.
- No git history, no fetching lists from the network, no remediation.

## FAQ

**Why exact versions and not semver ranges?**

Reports publish exact bad versions, not ranges. If `chalk@5.6.1` is
compromised, then `5.6.2` is probably the fix release and is safe.
Flagging it would be a false positive that makes every other hit harder
to trust.

Note that `^5.6.1` in `package.json` is a range, but `npm install`
resolves each range to a specific exact version, and that's what ends up
on disk and in the lockfile. This scanner reads what's installed, not
what was declared. So `chalk@5.6.1` in the list matches only installs
that resolved to exactly `5.6.1`. For the "no version of the package is
safe" case, use the `@*` wildcard.

**Why a binary instead of `npm audit`?**

`npm audit` runs per-project. This runs once across the whole machine
and checks caches too.

## License

MIT
