package httpapi_test

import (
	"bufio"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	routeLiteral = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) (/v1/\S*)$`)
	contractPath = regexp.MustCompile(`^  (/v1/\S+):\s*$`)
	contractOp   = regexp.MustCompile(`^    (get|post|put|patch|delete):\s*$`)
)

// registeredOperations returns every "METHOD /v1/…" string literal in the
// package source. Routes are registered from literals (directly or through
// tables), so a /v1 route cannot exist without appearing here.
func registeredOperations(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var ops []string
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err == nil && routeLiteral.MatchString(s) && !slices.Contains(ops, s) {
				ops = append(ops, s)
			}
			return true
		})
	}
	slices.Sort(ops)
	return ops
}

// contractOperations returns every "METHOD /v1/…" operation of
// api/openapi.yaml (paths at two spaces, methods at four).
func contractOperations(t *testing.T) []string {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var ops []string
	path := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := contractPath.FindStringSubmatch(line); m != nil {
			path = m[1]
			continue
		}
		if len(line) > 0 && line[0] != ' ' || strings.HasPrefix(line, "  ") && !strings.HasPrefix(line, "   ") {
			path = ""
		}
		if m := contractOp.FindStringSubmatch(line); m != nil && path != "" {
			ops = append(ops, strings.ToUpper(m[1])+" "+path)
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ops)
	return ops
}

// TestRegisteredRoutesMatchContract: the /v1 operations the server registers
// are exactly those of the published contract.
func TestRegisteredRoutesMatchContract(t *testing.T) {
	registered, contract := registeredOperations(t), contractOperations(t)
	if len(contract) < 30 {
		t.Fatalf("parsed only %d contract operations", len(contract))
	}
	for _, op := range registered {
		if !slices.Contains(contract, op) {
			t.Errorf("route %q is not in api/openapi.yaml", op)
		}
	}
	for _, op := range contract {
		if !slices.Contains(registered, op) {
			t.Errorf("contract operation %q is not registered", op)
		}
	}
}
