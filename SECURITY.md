# Security Policy

## Reporting a vulnerability

Please report security issues in npm-chainsaw **privately**, not as a public
issue. Open a private advisory through GitHub:

> Repository **Security** tab → **Report a vulnerability**

(<https://github.com/EliasPh/npm-chainsaw/security/advisories/new>)

You'll get an acknowledgement, and a fix or response on a best-effort basis —
this is a small, single-maintainer project, so please be patient.

## Scope and design guarantees

npm-chainsaw is a local, read-only scanner. Two properties are enforced, not
just intended:

- **Read-only.** It opens files for reading only. It never writes, deletes,
  renames, changes permissions, or executes other programs. This is checked at
  build time by [`readonly_test.go`](cmd/npm-chainsaw/readonly_test.go), which
  parses the scanner source and fails CI if a filesystem-mutating call or a
  process-spawning import (`os/exec`, `syscall`, …) appears. It is a denylist,
  not a sandbox — see that file's notes on what it does and doesn't catch.
- **No network.** The tool makes no network calls. It reads incident lists and
  installed packages from the local disk only; it never fetches lists or
  reports anything back.

A scan reporting `complete` / `exit 0` is a statement only about what it was
configured to read (the scan root, and caches unless `--no-cache`). It is not a
machine-wide guarantee. See "What `complete` means" in the README.

## Supported versions

Only the latest release is supported. Releases publish a `SHA256SUMS` file;
verify downloaded binaries against it, or install from source with `go install`.
