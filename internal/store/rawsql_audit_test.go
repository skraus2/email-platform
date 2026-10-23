package store

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// rawSQLFiles may run SQL that is not a generated query: they never touch
// project data, or are measurement tools outside the product.
var rawSQLFiles = map[string]string{
	"internal/database/database.go": "migration version check",
	"internal/testdb/testdb.go":     "creates and drops test databases",
	"cmd/loadtest/main.go":          "load measurement tool, reads results directly",
	"cmd/loadtest/broadcast.go":     "load measurement tool, seeds and reads directly",
}

// sqlStatement matches SQL as written in this code base: upper-case keywords.
var sqlStatement = regexp.MustCompile(`^\s*(SELECT|INSERT|UPDATE|DELETE|WITH|CREATE|DROP|ALTER|TRUNCATE)\b[\s\S]*\b(FROM|INTO|SET|VALUES|TABLE|DATABASE)\b`)

// TestNoRawSQLOutsideStore keeps every product query in db/queries, where the
// project-scope audit sees it, and the job queue implementation in
// internal/jobs, whose job args the args audit checks.
func TestNoRawSQLOutsideStore(t *testing.T) {
	root := filepath.Join("..", "..")
	seen := map[string]bool{}
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/store/") {
				return nil
			}
			f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly|parser.ParseComments)
			if err != nil {
				return err
			}
			for _, imp := range f.Imports {
				p, _ := strconv.Unquote(imp.Path.Value)
				if strings.HasPrefix(p, "github.com/riverqueue/river") && !strings.HasPrefix(rel, "internal/jobs/") {
					t.Errorf("%s imports %s; only internal/jobs may use the job queue", rel, p)
				}
			}
			f, err = parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				s, err := strconv.Unquote(lit.Value)
				if err != nil || !sqlStatement.MatchString(s) {
					return true
				}
				if _, ok := rawSQLFiles[rel]; ok {
					seen[rel] = true
					return true
				}
				t.Errorf("%s: SQL string %.40q outside db/queries", fset.Position(lit.Pos()), s)
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for name := range rawSQLFiles {
		if !seen[name] {
			t.Errorf("stale rawSQLFiles entry %s", name)
		}
	}
}
