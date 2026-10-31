package emailplatform

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const modulePath = "github.com/skraus2/email-platform/packages/sdk-go"

// The oldest Go release the module supports; moved deliberately when the
// supported releases (the latest two) move.
const goLine = "go 1.26.0"

// The module must stay installable without anything but the standard
// library: go.mod names the module and the oldest supported Go release only.
func TestGoModHasNoDependencies(t *testing.T) {
	data, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	var directives []string
	for line := range strings.Lines(string(data)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}
		directives = append(directives, strings.Fields(line)[0])
		if line != "module "+modulePath && line != goLine {
			t.Errorf("go.mod: unexpected line %q", line)
		}
	}
	if strings.Join(directives, " ") != "module go" {
		t.Errorf("go.mod directives = %v, want module and go only", directives)
	}
}

// Every import in the module is from the standard library or the module
// itself (never the platform's internal packages); no cgo, no unsafe.
func TestImportsAreStandardLibraryOnly(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			p, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			first, _, _ := strings.Cut(p, "/")
			own := p == modulePath || strings.HasPrefix(p, modulePath+"/")
			if p == "C" || p == "unsafe" || (strings.Contains(first, ".") && !own) {
				t.Errorf("%s imports %s", path, p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The platform module never depends on the SDK.
func TestPlatformDoesNotRequireTheSDK(t *testing.T) {
	data, err := os.ReadFile("../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), modulePath) {
		t.Fatal("the platform's go.mod refers to the SDK module")
	}
}
