# Incident lists

Each `.txt` file here describes one supply chain incident: header
comments at the top with the source link and date, then one
`package@version` (or `name@*`) per line.

See [`TEMPLATE.txt`](TEMPLATE.txt) for the format and a copy-paste
starting point.

## Contributing a new incident file

You don't need to know Go to contribute; the only thing you add is a
text file.

1. Copy `TEMPLATE.txt` to `YYYY-MM-<slug>.txt` in this directory.
   - `YYYY-MM` is the month the report was published.
   - `<slug>` is a short, lowercase, hyphen-separated identifier
     (e.g. `tanstack-mistral`, `shai-hulud`).
2. Fill in the header comments at the top of the file:
   - `source:` link to the original report or write-up
   - `date:` publication date (`YYYY-MM-DD`)
   - `summary:` one short line about what happened
3. List the compromised packages, one per line:
   - Exact version: `name@1.2.3`
   - Multiple versions: `name@1.2.3, 1.2.4, 1.2.5`
   - Wildcard: `name@*` (only when no version of the package can be
     trusted)
4. Verify the file parses by running the scanner against any directory:

   ```sh
   go run ./cmd/npm-chainsaw incidents/YYYY-MM-your-slug.txt /tmp
   ```

   You'll see a "Searching for N packages..." header listing every
   entry. Bad lines produce a parse error and exit code 2.
5. Open a PR with the new file. Suggested title:
   `incident: YYYY-MM <short name>`. Link to the source in the
   PR description.

A few small conventions to keep this directory tidy:

- One incident per file. Easier to update or remove a single file than
  to untangle a merged list.
- Exact versions only. No semver ranges like `^1.0.0`. See the
  [main README](../README.md#why-exact-versions-and-not-semver-ranges)
  for the reason.
- npm packages only.
