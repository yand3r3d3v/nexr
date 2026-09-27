// Package archtest checks the dependency rules of docs/architecture.md §3.
package archtest

import (
	"bufio"
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const module = "github.com/yand3r3d3v/nexr"

// layers ranks the internal packages. A package may import packages of the
// same or a lower rank only. A package missing here fails the test, so that
// every new package is placed deliberately.
var layers = map[string]int{
	// Shared: no dependencies on the layers above.
	"internal/errs":      0,
	"internal/buildinfo": 0,
	"internal/output":    0,
	"internal/config":    0,
	"internal/remote":    0,
	"internal/workpool":  0,
	// Infrastructure.
	"internal/httpx": 1,
	// API clients.
	"internal/nexus":           2,
	"internal/nexus/nexustest": 2,
	"internal/registry":        2,
	// Domain.
	"internal/files":     3,
	"internal/formats":   3,
	"internal/images":    3,
	"internal/retention": 3,
	"internal/tasks":     3,
	// Presentation and entry point.
	"internal/cli": 4,
	"cmd/nexr":     5,
	// Tests only.
	"internal/archtest": 6,
}

type pkg struct {
	path    string // relative to the module, e.g. "internal/cli/reposcmd"
	imports []string
	dir     string
}

func rel(importPath string) (string, bool) {
	if importPath == module {
		return "", true
	}
	return strings.CutPrefix(importPath, module+"/")
}

// rank returns the layer of a package; sub-packages of internal/cli share it.
func rank(path string) (int, bool) {
	if r, ok := layers[path]; ok {
		return r, true
	}
	if strings.HasPrefix(path, "internal/cli/") {
		return layers["internal/cli"], true
	}
	return 0, false
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func listPackages(t *testing.T) []pkg {
	t.Helper()
	root := moduleRoot(t)
	cmd := exec.Command("go", "list", "-f", `{{.ImportPath}}|{{.Dir}}|{{join .Imports " "}}`, "./...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	var pkgs []pkg
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		parts := strings.SplitN(sc.Text(), "|", 3)
		path, ok := rel(parts[0])
		if !ok {
			t.Fatalf("package %s is outside the module", parts[0])
		}
		pkgs = append(pkgs, pkg{path: path, dir: parts[1], imports: strings.Fields(parts[2])})
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func TestLayering(t *testing.T) {
	for _, p := range listPackages(t) {
		from, ok := rank(p.path)
		if !ok {
			t.Errorf("%s is not assigned to a layer in archtest.layers", p.path)
			continue
		}
		for _, imp := range p.imports {
			target, internal := rel(imp)
			if !internal {
				continue
			}
			to, ok := rank(target)
			if !ok {
				continue // reported for the package itself
			}
			if to > from {
				t.Errorf("%s (layer %d) imports %s (layer %d): dependencies must point downwards", p.path, from, target, to)
			}
		}
	}
}

func TestOnlyCLIUsesCobra(t *testing.T) {
	for _, p := range listPackages(t) {
		if p.path == "cmd/nexr" || p.path == "internal/cli" || strings.HasPrefix(p.path, "internal/cli/") {
			continue
		}
		for _, imp := range p.imports {
			if strings.HasPrefix(imp, "github.com/spf13/") {
				t.Errorf("%s imports %s; only cmd/nexr and internal/cli may", p.path, imp)
			}
		}
	}
}

func TestNexusFakeIsIndependent(t *testing.T) {
	// The fake must not share code with the client it verifies.
	for _, p := range listPackages(t) {
		if p.path != "internal/nexus/nexustest" {
			continue
		}
		for _, imp := range p.imports {
			if _, internal := rel(imp); internal {
				t.Errorf("nexustest imports %s", imp)
			}
		}
	}
}

// TestOnlyHTTPXBuildsClients looks for http.Client composite literals in the
// non-test sources of every package but internal/httpx.
func TestOnlyHTTPXBuildsClients(t *testing.T) {
	for _, p := range listPackages(t) {
		if p.path == "internal/httpx" {
			continue
		}
		files, err := filepath.Glob(filepath.Join(p.dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
			if err != nil {
				t.Fatal(err)
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if sel, ok := lit.Type.(*ast.SelectorExpr); ok && sel.Sel.Name == "Client" {
					if x, ok := sel.X.(*ast.Ident); ok && x.Name == "http" {
						t.Errorf("%s builds an http.Client; use httpx.NewClient", fset.Position(lit.Pos()))
					}
				}
				return true
			})
		}
	}
}
