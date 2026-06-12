// Command npm-chainsaw scans a machine for npm packages with known-bad
// versions. See README.md for usage.
package main

import "os"

var version = "dev"

func main() {
	// Thin wrapper around runCLI so the exit code is testable.
	os.Exit(runCLI(os.Args[1:]))
}
