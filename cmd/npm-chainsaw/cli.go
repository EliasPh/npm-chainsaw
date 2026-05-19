package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

// splitFlagsAndPositionals separates argv tokens so flags can appear anywhere
// on the command line, not just before the positional arguments. Go's stdlib
// flag.Parse stops at the first non-flag arg, which means
//
//	npm-chainsaw list.txt --verbose
//
// would silently treat "--verbose" as a second positional path. All our
// flags are booleans so we can split with a simple prefix check.
func splitFlagsAndPositionals(args []string) (flagArgs, positional []string) {
	for _, a := range args {
		if strings.HasPrefix(a, "-") && a != "-" && a != "--" {
			flagArgs = append(flagArgs, a)
		} else {
			positional = append(positional, a)
		}
	}
	return
}

// cliOpts holds the parsed CLI inputs.
type cliOpts struct {
	listPath string // path to incident list (positional 1, required)
	scanRoot string // directory to walk (positional 2, defaults to $HOME)
	jsonOut  bool   // --json
	verbose  bool   // --verbose
	noCache  bool   // --no-cache
}

// runCLI parses args, dispatches the run, and returns the process exit code.
//
// Exit codes (per SPEC.md):
//
//	0  clean, no hits (also: --version, --help)
//	1  hits found
//	2  error (bad flags, missing/unreadable input, parse error)
func runCLI(args []string) int {
	fs := flag.NewFlagSet("npm-chainsaw", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() { printUsage(fs.Output()) }

	showVersion := fs.Bool("version", false, "Print version and exit")
	jsonOut := fs.Bool("json", false, "Emit machine-readable JSON output")
	verbose := fs.Bool("verbose", false, "Show all hit locations")
	noCache := fs.Bool("no-cache", false, "Skip npm/pnpm/yarn caches")

	flagArgs, positional := splitFlagsAndPositionals(args)
	if err := fs.Parse(flagArgs); err != nil {
		// --help triggers ErrHelp; fs.Usage has already printed the message.
		if err == flag.ErrHelp {
			return 0
		}
		return 2
	}

	if *showVersion {
		fmt.Println("npm-chainsaw", version)
		return 0
	}

	if len(positional) < 1 {
		printUsage(os.Stderr)
		return 2
	}

	// $HOME is needed for both the default scan root and cache scanning.
	// An empty home (rare, but possible) just means we skip cache scanning.
	home, _ := os.UserHomeDir()

	opts := cliOpts{
		listPath: positional[0],
		jsonOut:  *jsonOut,
		verbose:  *verbose,
		noCache:  *noCache,
	}
	if len(positional) >= 2 {
		opts.scanRoot = positional[1]
	} else if home != "" {
		opts.scanRoot = home
	} else {
		fmt.Fprintln(os.Stderr, "error: no scan path given and $HOME unknown")
		return 2
	}

	// Load the incident list. Errors here are fatal (exit 2).
	f, err := os.Open(opts.listPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 2
	}
	defer f.Close()
	targets, count, err := parseTargets(f)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing %s: %v\n", opts.listPath, err)
		return 2
	}

	// Print the search targets so the user knows what's being checked
	// before the (potentially long) scan starts. JSON output is already
	// self-describing, so skip it there.
	if !opts.jsonOut {
		printSearchHeader(os.Stderr, opts.listPath, targets, count)
	}

	// Shared atomic counter between the walker and the progress goroutine,
	// if any. Counter writes only from the walker; reads happen during the
	// progress loop and at the end of scan().
	start := time.Now()
	var counter atomic.Int64
	progressDone := make(chan struct{})
	if shouldShowProgress(opts.jsonOut) {
		go progressLoop(&counter, progressDone)
	}

	hits, inspected, err := scan(opts.scanRoot, targets, &counter)
	if err != nil {
		close(progressDone)
		fmt.Fprintln(os.Stderr, "error during scan:", err)
		return 2
	}
	if !opts.noCache && home != "" {
		hits = append(hits, scanCaches(home, targets)...)
	}
	close(progressDone)
	dur := time.Since(start)

	if opts.jsonOut {
		if err := printJSON(os.Stdout, hits, targets, inspected, dur); err != nil {
			fmt.Fprintln(os.Stderr, "error encoding json:", err)
			return 2
		}
	} else {
		printHuman(os.Stdout, hits, targets, inspected, dur, opts.verbose, colorEnabled(os.Stdout))
	}

	if len(hits) > 0 {
		return 1
	}
	return 0
}

// printUsage writes the help text. Kept terse on purpose; full docs live in
// README.md.
func printUsage(w io.Writer) {
	fmt.Fprint(w, `npm-chainsaw: scan for compromised npm packages.

Usage:
  npm-chainsaw <list.txt> [path] [flags]

Arguments:
  list.txt      Incident list (see incidents/README.md for the format)
  path          Scan root (default: $HOME)

Flags:
  --json        Machine-readable output
  --verbose     Show all hit locations
  --no-cache    Skip npm/pnpm/yarn caches
  --version     Print version
  --help        Show this message

Exit codes:
  0  no hits
  1  hits found
  2  error
`)
}
