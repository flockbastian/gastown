package pidalive

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// lintAllowlist names files (relative to internal/, slash-separated) that probe liveness with kill(pid, 0) directly
// but already treat EPERM as alive. Every entry needs a one-line reason.
var lintAllowlist = map[string]string{
	"util/orphan.go":      "checks err == nil || err == syscall.EPERM inline",
	"cmd/process_unix.go": "isProcessRunning returns true on EPERM",
}

const lintAdvice = "treats EPERM as dead: under a sandbox (srt) or for another user's process kill(pid, 0) returns EPERM " +
	"for a live process. Use pidalive.Alive(pid) or pidalive.Proc(proc) instead " +
	"(see https://github.com/flockbastian/gt-hq/blob/main/docs/sandbox.md section 4.1)"

// TestLintNoRawLivenessProbes fails on Signal(0)/Kill(pid, 0) liveness probes outside this package.
func TestLintNoRawLivenessProbes(t *testing.T) {
	internalDir := ".."
	used := map[string]bool{}
	var failures []string

	err := filepath.WalkDir(internalDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != internalDir && d.Name() == "pidalive" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(internalDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		for _, pos := range rawLivenessProbes(fset, file) {
			if _, ok := lintAllowlist[rel]; ok {
				used[rel] = true
				continue
			}
			failures = append(failures, fmt.Sprintf("internal/%s:%d: %s", rel, pos.Line, lintAdvice))
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk internal/: %v", err)
	}

	for _, f := range failures {
		t.Error(f)
	}
	var stale []string
	for rel := range lintAllowlist {
		if !used[rel] {
			stale = append(stale, rel)
		}
	}
	sort.Strings(stale)
	for _, rel := range stale {
		t.Errorf("lintAllowlist entry %q no longer contains a raw liveness probe; remove it", rel)
	}
}

// TestLintDetector keeps the lint honest: it must flag the old idioms and leave unrelated calls alone.
func TestLintDetector(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"process signal zero", `func f(p *os.Process) bool { return p.Signal(syscall.Signal(0)) == nil }`, 1},
		{"kill zero", `func f(pid int) bool { return syscall.Kill(pid, 0) == nil }`, 1},
		{"unix kill zero", `func f(pid int) bool { return unix.Kill(pid, 0) == nil }`, 1},
		{"err != nil form", `func f(p *os.Process) bool { err := p.Signal(syscall.Signal(0)); return err != nil }`, 1},
		{"sigterm", `func f(p *os.Process) { _ = p.Signal(syscall.SIGTERM) }`, 0},
		{"kill sigterm", `func f(pid int) { _ = syscall.Kill(pid, 15) }`, 0},
		{"pidalive", `func f(pid int) bool { return pidalive.Alive(pid) }`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fset := token.NewFileSet()
			src := "package p\n" + c.body + "\n"
			file, err := parser.ParseFile(fset, "p.go", src, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			if got := len(rawLivenessProbes(fset, file)); got != c.want {
				t.Errorf("rawLivenessProbes found %d probes, want %d", got, c.want)
			}
		})
	}
}

// rawLivenessProbes returns the positions of x.Signal(syscall.Signal(0)) and syscall.Kill(pid, 0) calls in file.
func rawLivenessProbes(fset *token.FileSet, file *ast.File) []token.Position {
	var found []token.Position
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Signal":
			if len(call.Args) == 1 && isSignalZeroConversion(call.Args[0]) {
				found = append(found, fset.Position(call.Pos()))
			}
		case "Kill":
			pkg, ok := sel.X.(*ast.Ident)
			if ok && (pkg.Name == "syscall" || pkg.Name == "unix") && len(call.Args) == 2 && isZeroLiteral(call.Args[1]) {
				found = append(found, fset.Position(call.Pos()))
			}
		}
		return true
	})
	return found
}

// isSignalZeroConversion matches syscall.Signal(0).
func isSignalZeroConversion(e ast.Expr) bool {
	conv, ok := e.(*ast.CallExpr)
	if !ok || len(conv.Args) != 1 || !isZeroLiteral(conv.Args[0]) {
		return false
	}
	sel, ok := conv.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Signal" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && (pkg.Name == "syscall" || pkg.Name == "unix")
}

func isZeroLiteral(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.INT && lit.Value == "0"
}
