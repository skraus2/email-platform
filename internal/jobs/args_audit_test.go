package jobs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// globalJobArgs are job kinds that act on all projects at once (maintenance),
// so their args carry no project.
var globalJobArgs = map[string]string{
	"cleanupArgs": "periodic maintenance over all projects (expiry, retention)",
}

// foreignProjectTests name, per job args type, the integration test proving
// that its handler leaves a row alone when the job's project does not own it.
var foreignProjectTests = map[string]string{
	"SendEmailArgs":         "internal/emails/dispatch_integration_test.go:TestDispatchIsScopedByProject",
	"DeliverWebhookArgs":    "internal/webhooks/deliver_integration_test.go:TestDeliverRefusals",
	"BroadcastFanoutArgs":   "internal/broadcasts/fanout_integration_test.go:TestFanoutIgnoresForeignProject",
	"AutomationStepArgs":    "internal/automations/engine_integration_test.go:TestEngineIgnoresForeignProject",
	"AutomationArchiveArgs": "internal/automations/archive_integration_test.go:TestArchiveCancelsRunsInBatches",
}

// TestJobArgsCarryProject finds every job args type of this package (any type
// with a Kind() string method, so a new kind cannot be missed) and requires a
// ProjectID uuid.UUID field serialised as "project_id": workers scope every
// query by the job's project.
func TestJobArgsCarryProject(t *testing.T) {
	fset := token.NewFileSet()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	structs := map[string]*ast.StructType{}
	kinds := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(".", e.Name()), nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						if st, ok := ts.Type.(*ast.StructType); ok {
							structs[ts.Name.Name] = st
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv == nil || d.Name.Name != "Kind" || len(d.Recv.List) != 1 {
					continue
				}
				if res := d.Type.Results; res == nil || len(res.List) != 1 || types(res.List[0].Type) != "string" {
					continue
				}
				kinds[types(d.Recv.List[0].Type)] = true
			}
		}
	}
	if len(kinds) < 6 {
		t.Fatalf("found only %d job kinds: %v", len(kinds), kinds)
	}
	for name := range kinds {
		st, ok := structs[name]
		if !ok {
			t.Errorf("job args %s: not a struct", name)
			continue
		}
		if _, global := globalJobArgs[name]; global {
			continue
		}
		if !hasProjectID(st) {
			t.Errorf("job args %s has no ProjectID uuid.UUID `json:\"project_id\"` field", name)
		}
	}
	for name := range globalJobArgs {
		if !kinds[name] {
			t.Errorf("stale global job entry %s", name)
		}
	}
	for name := range kinds {
		if _, global := globalJobArgs[name]; global {
			continue
		}
		ref, ok := foreignProjectTests[name]
		if !ok {
			t.Errorf("job args %s: no test proving its handler ignores a foreign project", name)
			continue
		}
		file, fn, _ := strings.Cut(ref, ":")
		src, err := os.ReadFile(filepath.Join("..", "..", file))
		if err != nil || !strings.Contains(string(src), "func "+fn+"(t *testing.T)") {
			t.Errorf("job args %s: test %s not found (%v)", name, ref, err)
		}
	}
	for name := range foreignProjectTests {
		if !kinds[name] {
			t.Errorf("stale foreign project test entry %s", name)
		}
	}
}

func hasProjectID(st *ast.StructType) bool {
	for _, f := range st.Fields.List {
		if types(f.Type) != "uuid.UUID" || f.Tag == nil || len(f.Names) != 1 || f.Names[0].Name != "ProjectID" {
			continue
		}
		raw, err := strconv.Unquote(f.Tag.Value)
		if err != nil {
			continue
		}
		tag := reflect.StructTag(raw)
		if tag.Get("json") == "project_id" {
			return true
		}
	}
	return false
}

// types renders a type expression of the forms used for receivers and fields.
func types(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.Ident:
		return x.Name
	case *ast.StarExpr:
		return types(x.X)
	case *ast.SelectorExpr:
		return types(x.X) + "." + x.Sel.Name
	}
	return ""
}
