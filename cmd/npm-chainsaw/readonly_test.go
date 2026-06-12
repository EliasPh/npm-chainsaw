package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Enforces the read-only promise at build time. Parses every non-test .go
// file in the package and fails if it finds a write call or a risky import.
//
// Banning the constructors is enough: the only ways to get a writable
// *os.File are os.Create and os.OpenFile, both denied, so there's no path to
// a file.Write call. Output (fmt.Fprintf to stdout) and strings.Builder are
// not filesystem writes, so they're fine.

// forbiddenOsCalls are os.* functions that write or mutate the filesystem.
// Read-only calls (os.Open, os.ReadFile, os.ReadDir, os.Stat) are allowed.
var forbiddenOsCalls = map[string]bool{
	"Create":      true,
	"CreateTemp":  true,
	"OpenFile":    true, // os.Open is fine; OpenFile can request write flags
	"WriteFile":   true,
	"WriteString": true,
	"Mkdir":       true,
	"MkdirAll":    true,
	"MkdirTemp":   true,
	"Remove":      true,
	"RemoveAll":   true,
	"Rename":      true,
	"Chmod":       true,
	"Chown":       true,
	"Lchown":      true,
	"Truncate":    true,
	"Symlink":     true,
	"Link":        true,
	"Mkfifo":      true,
	"Pipe":        true,
}

// forbiddenImports route around the os.* denylist to write or run programs.
var forbiddenImports = map[string]bool{
	"os/exec":   true,
	"syscall":   true,
	"io/ioutil": true, // ioutil.WriteFile et al.
}

func TestReadOnly_NoFilesystemMutation(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("globbing package files: %v", err)
	}

	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue // tests legitimately create fixtures in temp dirs
		}
		checked++
		checkFileReadOnly(t, file)
	}

	// Don't let the guard pass by checking nothing if the glob ever breaks.
	if checked == 0 {
		t.Fatal("read-only guard inspected zero source files; the glob is wrong")
	}
}

func checkFileReadOnly(t *testing.T, file string) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}

	// Track the local name of the "os" import so an alias can't dodge the check.
	osName := ""
	for _, imp := range f.Imports {
		path := strings.Trim(imp.Path.Value, `"`)
		if forbiddenImports[path] {
			pos := fset.Position(imp.Pos())
			t.Errorf("%s:%d: forbidden import %q: npm-chainsaw must stay read-only",
				file, pos.Line, path)
		}
		if path == "os" {
			if imp.Name != nil {
				osName = imp.Name.Name
			} else {
				osName = "os"
			}
		}
	}

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || osName == "" || pkg.Name != osName {
			return true
		}
		if forbiddenOsCalls[sel.Sel.Name] {
			pos := fset.Position(call.Pos())
			t.Errorf("%s:%d: forbidden call %s.%s: npm-chainsaw must stay read-only "+
				"(only file reads are allowed; see readonly_test.go)",
				file, pos.Line, osName, sel.Sel.Name)
		}
		return true
	})
}

// Sanity check that the guard can actually see the package source.
func TestReadOnly_GuardIsWired(t *testing.T) {
	if _, err := os.Stat("scanner.go"); err != nil {
		t.Fatalf("expected scanner.go in package dir: %v", err)
	}
}
