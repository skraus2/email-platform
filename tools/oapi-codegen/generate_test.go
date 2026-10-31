package main

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"gopkg.in/yaml.v3"
)

const (
	contractPath = "../../api/openapi.yaml"
	overlayPath  = "../../packages/sdk-go/overlay.yaml"
	sdkDir       = "../../packages/sdk-go"
)

// A small valid contract; the tests add the construct under test.
const miniContract = `openapi: 3.1.0
info: {title: t, version: "1"}
paths: {}
components:
  schemas:
    B: {type: object, properties: {k: {type: string}}}
`

const emptyOverlay = `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: $.components.schemas.B
    update: {description: b}
`

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCommittedFilesMatchTheContract(t *testing.T) {
	files, err := generate(contractPath, overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkDrift(files, sdkDir); err != nil {
		t.Fatal(err)
	}
}

func TestContractChangeWithoutRegenerationIsDrift(t *testing.T) {
	dir := t.TempDir()
	contract, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(contract), "    Contact:\n", "    Contact:\n      description: changed\n", 1)
	if changed == string(contract) {
		t.Fatal("test edit did not apply")
	}
	changedPath := writeFile(t, dir, "openapi.yaml", changed)
	files, err := generate(changedPath, overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	err = checkDrift(files, sdkDir)
	if err == nil || !strings.Contains(err.Error(), "types.gen.go") || !strings.Contains(err.Error(), regenerate) {
		t.Fatalf("checkDrift = %v, want drift naming types.gen.go and %q", err, regenerate)
	}
	if strings.Contains(err.Error(), operationsFile) {
		t.Fatalf("checkDrift = %v, operations did not change", err)
	}
}

func TestMissingFileIsDrift(t *testing.T) {
	files := map[string][]byte{typesFile: []byte("package emailplatform\n")}
	err := checkDrift(files, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), typesFile) {
		t.Fatalf("checkDrift = %v, want drift naming %s", err, typesFile)
	}
}

func TestRunWritesFilesThatThenPassTheCheck(t *testing.T) {
	dir := t.TempDir()
	if err := run(contractPath, overlayPath, dir, false); err != nil {
		t.Fatal(err)
	}
	if err := run(contractPath, overlayPath, dir, true); err != nil {
		t.Fatalf("check after write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, operationsFile), []byte("package emailplatform\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run(contractPath, overlayPath, dir, true); err == nil || !strings.Contains(err.Error(), operationsFile) {
		t.Fatalf("check after edit = %v, want drift naming %s", err, operationsFile)
	}
}

func TestNonLocalReferenceInContract(t *testing.T) {
	refused := map[string]string{
		"remote $ref":           `    A: {$ref: "https://example.com/x.yaml#/A"}`,
		"file $ref":             `    A: {$ref: "./other.yaml#/B"}`,
		"absolute file $ref":    `    A: {$ref: "/etc/passwd"}`,
		"non-string $ref":       `    A: {$ref: 1}`,
		"escaped $ref key":      `    A: {"\x24ref": "./other.yaml#/B"}`,
		"mapping via an alias":  "    A: {x-m: &m {x: ./other.yaml#/B}, oneOf: [{$ref: \"#/components/schemas/B\"}], discriminator: {propertyName: k, mapping: *m}}",
		"upper-case $REF":       `    A: {$REF: "./other.yaml#/B"}`,
		"mixed-case $Id":        `    A: {$Id: "https://example.com/a"}`,
		"upper-case MAPPING":    `    A: {oneOf: [{$ref: "#/components/schemas/B"}], Discriminator: {propertyName: k, MAPPING: {x: ./other.yaml#/B}}}`,
		"$id":                   `    A: {$id: "https://example.com/a"}`,
		"$dynamicRef":           `    A: {$dynamicRef: "#meta"}`,
		"externalValue":         `    A: {examples: [{externalValue: "https://example.com/e.json"}]}`,
		"mapping to a file":     `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: {x: ./other.yaml#/B}}}`,
		"mapping to a URL":      `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: {x: "https://example.com/b"}}}`,
		"mapping not a map":     `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: "./other.yaml"}}`,
		"$ref in discriminator": `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {$ref: "#/components/schemas/B"}}`,
		"$ref in mapping":       `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: {$ref: "#/components/schemas/B"}}}`,
	}
	for name, schema := range refused {
		t.Run(name, func(t *testing.T) {
			reason, err := nonLocalInContract([]byte(miniContract + schema + "\n"))
			if err != nil {
				t.Fatal(err)
			}
			if reason == "" {
				t.Fatal("not refused")
			}
		})
	}
	allowed := map[string]string{
		"local $ref":             `    A: {$ref: "#/components/schemas/B"}`,
		"local mapping":          `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: {x: "#/components/schemas/B"}}}`,
		"$ref in a description":  `    A: {description: "$ref: ./other.yaml"}`,
		"property named mapping": `    A: {properties: {mapping: {type: string}}}`,
	}
	for name, schema := range allowed {
		t.Run(name, func(t *testing.T) {
			reason, err := nonLocalInContract([]byte(miniContract + schema + "\n"))
			if err != nil || reason != "" {
				t.Fatalf("refused: %q, %v", reason, err)
			}
		})
	}
}

func TestNonLocalReferenceInJSONContract(t *testing.T) {
	// kin-openapi decodes JSON with encoding/json first; the walk must see a
	// \u-escaped key as the $ref it decodes to.
	contract := "{\"openapi\":\"3.1.0\",\"info\":{\"title\":\"t\",\"version\":\"1\"},\"paths\":{}," +
		"\"components\":{\"schemas\":{\"A\":{\"\\u0024ref\":\"./other.yaml#/B\"}}}}"
	if !strings.Contains(contract, `\u0024ref`) {
		t.Fatal("test contract lost its escape")
	}
	reason, err := nonLocalInContract([]byte(contract))
	if err != nil || reason == "" {
		t.Fatalf("not refused: %q, %v", reason, err)
	}
}

func TestTheContractAndOverlayHaveOnlyLocalReferences(t *testing.T) {
	for path, check := range map[string]func([]byte) (string, error){
		contractPath: nonLocalInContract,
		overlayPath:  nonLocalInOverlay,
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if reason, err := check(data); err != nil || reason != "" {
			t.Fatalf("%s: %q, %v", path, reason, err)
		}
	}
}

func TestNonLocalReferenceInOverlay(t *testing.T) {
	refused := map[string]string{
		"extends": "extends: https://example.com/openapi.yaml\n" + emptyOverlay,
		"update with a file $ref": `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: $.components.schemas.B
    update: {properties: {p: {$ref: ./other.yaml#/B}}}
`,
		"update with $id": `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: $.components.schemas.B
    update: {$id: "https://example.com/b"}
`,
	}
	for name, overlay := range refused {
		t.Run(name, func(t *testing.T) {
			reason, err := nonLocalInOverlay([]byte(overlay))
			if err != nil || reason == "" {
				t.Fatalf("not refused: %q, %v", reason, err)
			}
		})
	}
}

func TestOverlaidContractIsWalkedBeforeLoading(t *testing.T) {
	// Values an overlay places inside a discriminator are judged where they
	// end up, not where they sit in the overlay file.
	dir := t.TempDir()
	contract := writeFile(t, dir, "openapi.yaml", miniContract+
		`    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k}}`+"\n")
	updates := map[string]string{
		"mapping to a file":     "{mapping: {x: ./other.yaml#/B}}",
		"$ref in discriminator": `{$ref: "#/components/schemas/B"}`,
	}
	for name, update := range updates {
		t.Run(name, func(t *testing.T) {
			overlay := writeFile(t, dir, "overlay.yaml", `overlay: 1.0.0
info: {title: t, version: "1"}
actions:
  - target: $.components.schemas.A.discriminator
    update: `+update+"\n")
			if reason, err := nonLocalInOverlay(mustRead(t, overlay)); err != nil || reason != "" {
				t.Fatalf("the overlay alone should pass the walk: %q, %v", reason, err)
			}
			_, err := generate(contract, overlay)
			if err == nil || !strings.Contains(err.Error(), "overlaid contract") {
				t.Fatalf("generate = %v, want the overlaid contract refused", err)
			}
		})
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestGenerateRefusesBeforeLoading(t *testing.T) {
	dir := t.TempDir()
	contract := writeFile(t, dir, "openapi.yaml", miniContract+`    A: {$ref: "./missing.yaml#/B"}`+"\n")
	overlay := writeFile(t, dir, "overlay.yaml", emptyOverlay)
	_, err := generate(contract, overlay)
	if err == nil || !strings.Contains(err.Error(), "is not a local reference") {
		t.Fatalf("generate = %v, want the reference refused", err)
	}
	bad := writeFile(t, dir, "bad-overlay.yaml", "extends: ./x.yaml\n"+emptyOverlay)
	_, err = generate(writeFile(t, dir, "ok.yaml", miniContract), bad)
	if err == nil || !strings.Contains(err.Error(), "extends") {
		t.Fatalf("generate = %v, want extends refused", err)
	}
}

func TestLoaderReadsNothing(t *testing.T) {
	// Whatever the walk misses, the loader must refuse rather than read: the
	// referenced file exists, so a read would succeed.
	dir := t.TempDir()
	writeFile(t, dir, "other.yaml", "B: {type: string}\n")
	path := filepath.Join(dir, "openapi.yaml")
	cases := map[string]string{
		"schema $ref":     `    A: {$ref: "./other.yaml#/B"}`,
		"mapping to file": `    A: {oneOf: [{$ref: "#/components/schemas/B"}], discriminator: {propertyName: k, mapping: {x: ./other.yaml#/B}}}`,
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := load(path, []byte(miniContract+schema+"\n"))
			if err == nil {
				t.Fatal("load succeeded, want the external reference refused")
			}
		})
	}
}

func TestLoaderDoesNotReReadTheContract(t *testing.T) {
	// kin-openapi resolves a local reference it cannot find in memory by
	// reading the document's location again. The copy on disk has the
	// schema, so only a refused read keeps the reference dangling.
	dir := t.TempDir()
	path := writeFile(t, dir, "openapi.yaml", miniContract+"    Missing: {type: string}\n")
	inMemory := []byte(miniContract + `    A: {$ref: "#/components/schemas/Missing"}` + "\n")

	reads := 0
	counting := func(l *openapi3.Loader, location *url.URL) ([]byte, error) {
		reads++
		return refuseRead(l, location)
	}
	if _, err := loadWith(path, inMemory, counting); err == nil {
		t.Fatal("load resolved the reference from disk")
	}
	if reads == 0 {
		t.Fatal("the loader did not try to read; the test no longer exercises the re-read")
	}
	if _, err := load(path, inMemory); err == nil {
		t.Fatal("load resolved the reference from disk")
	}
}

func TestOverlayIsStrict(t *testing.T) {
	dir := t.TempDir()
	contract := writeFile(t, dir, "openapi.yaml", miniContract)
	overlay := writeFile(t, dir, "overlay.yaml", strings.Replace(emptyOverlay, "schemas.B", "schemas.Gone", 1))
	_, err := generate(contract, overlay)
	if err == nil || !strings.Contains(err.Error(), "apply overlay") {
		t.Fatalf("generate = %v, want the missing target to fail", err)
	}
}

func TestCheckGenerated(t *testing.T) {
	const pkg = "package emailplatform\n"
	const valid = "func (e E) Valid() bool {\n\tswitch e {\n\tcase A:\n\t\treturn true\n\tdefault:\n\t\treturn false\n\t}\n}\n"
	refusedTypes := map[string]string{
		"import outside the allowlist":    pkg + "import \"os\"\nvar _ = os.Args\n",
		"import C":                        pkg + "import \"C\"\n",
		"blank import":                    pkg + "import _ \"time\"\n",
		"dot import":                      pkg + "import . \"time\"\ntype T Time\n",
		"func init":                       pkg + "func init() {}\n",
		"plain function":                  pkg + "func f() {}\n",
		"go:linkname":                     pkg + "//go:linkname x runtime.x\ntype x int\n",
		"go:embed":                        pkg + "//go:embed secret\ntype x string\n",
		"go:generate":                     pkg + "//go:generate sh -c x\n",
		"line directive":                  pkg + "//line other.go:1\ntype x int\n",
		"block comment":                   pkg + "type x /* a */ int\n",
		"other package":                   "package other\n",
		"var declaration":                 pkg + "var x int\n",
		"var with an initialiser":         pkg + "var _ = func() int { for {} }()\n",
		"const with a call":               pkg + "type E string\nconst A E = E(\"a\")\n",
		"const without a value":           pkg + "const (\n\tA = \"a\"\n\tB\n)\n",
		"MarshalJSON method":              pkg + "import \"encoding/json\"\ntype T struct{}\nfunc (T) MarshalJSON() ([]byte, error) { return json.Marshal(1) }\n",
		"Valid that loops":                pkg + "type E string\nfunc (e E) Valid() bool { for {} }\n",
		"Valid with a call":               pkg + "type E string\nconst A E = \"a\"\nfunc (e E) Valid() bool {\n\tswitch e {\n\tcase A:\n\t\treturn len(e) > 0\n\t}\n\treturn false\n}\n",
		"Valid on another value":          pkg + "type E string\nconst A E = \"a\"\nvar v E\n" + strings.Replace(valid, "switch e", "switch v", 1),
		"Valid with arguments":            pkg + "type E string\nconst A E = \"a\"\n" + strings.Replace(valid, "Valid()", "Valid(x int)", 1),
		"array length from a call":        pkg + "type T [len(\"ab\")]int\n",
		"embedded field":                  pkg + "import \"encoding/json\"\ntype T struct{ json.Unmarshaler }\n",
		"embedded field from time":        pkg + "import \"time\"\ntype T struct {\n\ttime.Time\n\tA string\n}\n",
		"interface with a method":         pkg + "type T interface{ M() }\n",
		"interface with an embed":         pkg + "import \"encoding/json\"\ntype T interface{ json.Marshaler }\n",
		"unexported type":                 pkg + "type e string\n",
		"type shadowing a builtin":        pkg + "type error string\n",
		"unexported constant":             pkg + "type E string\nconst a E = \"a\"\n",
		"iota constant":                   pkg + "type E int\nconst A E = iota\n",
		"pointer receiver":                pkg + "type E string\nconst A E = \"a\"\n" + strings.Replace(valid, "(e E)", "(e *E)", 1),
		"generic receiver":                pkg + "type E[T any] string\nconst A E[int] = \"a\"\n" + strings.Replace(valid, "(e E)", "(e E[T])", 1),
		"Valid on a type of another file": pkg + "const A E = \"a\"\n" + valid,
		"Valid case not a constant":       pkg + "type E string\nvar A E\n" + valid,
		"Valid case of another type":      pkg + "type E string\ntype F string\nconst A F = \"a\"\n" + valid,
		"Valid case returns false":        pkg + "type E string\nconst A E = \"a\"\n" + strings.Replace(valid, "return true", "return false", 1),
		"Valid default returns true":      pkg + "type E string\nconst A E = \"a\"\n" + strings.Replace(valid, "return false", "return true", 1),
		"Valid with a label":              pkg + "type E string\nconst A E = \"a\"\n" + strings.Replace(valid, "\tswitch e", "L:\n\tswitch e", 1),
		"bidi control in a comment":       pkg + "// a \u202e b\ntype T string\n",
		"bidi isolate in a string":        pkg + "type E string\nconst A E = \"a\u2066\"\n",
		"zero-width space":                pkg + "type E string\nconst A E = \"a\u200b\"\n",
	}
	for name, src := range refusedTypes {
		t.Run(name, func(t *testing.T) {
			if err := checkGenerated(typesFile, []byte(src)); err == nil {
				t.Fatal("not refused")
			}
		})
	}
	ok := pkg + "import (\n\t\"encoding/json\"\n\t\"time\"\n)\n" +
		"type T struct {\n\tA json.RawMessage `json:\"a\"`\n\tB *time.Time\n\tC map[string]any\n}\n" +
		"type E string\nconst (\n\tA E = \"a\"\n\tB E = \"b\"\n)\n" + valid
	if err := checkGenerated(typesFile, []byte(ok)); err != nil {
		t.Fatal(err)
	}

	const ops = pkg + "type operation struct{ id, method, path string }\nvar operations = [...]operation{\n\t{\"a\", \"GET\", \"/v1/a\"},\n}\n"
	if err := checkGenerated(operationsFile, []byte(ops)); err != nil {
		t.Fatal(err)
	}
	refusedOps := map[string]string{
		"an import":                pkg + "import \"time\"\n",
		"a call in the table":      strings.Replace(ops, `"a",`, `string(rune(97)),`, 1),
		"a second var":             ops + "var x = 1\n",
		"another type":             ops + "type x int\n",
		"a method":                 ops + "func (operation) String() string { return \"\" }\n",
		"a func literal":           strings.Replace(ops, `"a",`, `func() string { return "a" }(),`, 1),
		"array length from a call": strings.Replace(ops, "[...]operation", `[len("x")]operation`, 1),
		"a typed table":            strings.Replace(ops, "var operations =", "var operations [1]operation =", 1),
		"a table of another type":  strings.Replace(ops, "[...]operation{", "[...]struct{ id, method, path string }{", 1),
		"a bidi control":           strings.Replace(ops, `"/v1/a"`, "\"/v1/a\u202e\"", 1),
	}
	for name, src := range refusedOps {
		t.Run("operations "+name, func(t *testing.T) {
			if err := checkGenerated(operationsFile, []byte(src)); err == nil {
				t.Fatal("not refused")
			}
		})
	}
}

func TestCommittedFilesPassTheStaticCheck(t *testing.T) {
	for _, name := range []string{typesFile, operationsFile} {
		src, err := os.ReadFile(filepath.Join(sdkDir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := checkGenerated(name, src); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOperationsAreTheContractsPathOperations(t *testing.T) {
	// Read independently of kin-openapi: every operationId under paths.
	data, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	var contract struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
		} `yaml:"paths"`
	}
	if err := yaml.Unmarshal(data, &contract); err != nil {
		t.Fatal(err)
	}
	var want []string
	for path, methods := range contract.Paths {
		for method, op := range methods {
			if op.OperationID != "" {
				want = append(want, op.OperationID+" "+strings.ToUpper(method)+" "+path)
			}
		}
	}
	slices.Sort(want)
	if len(want) != 37 {
		t.Fatalf("contract has %d operations, want 37", len(want))
	}

	files, err := generate(contractPath, overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for line := range strings.Lines(string(files[operationsFile])) {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `{"`) {
			continue
		}
		fields := strings.Split(strings.Trim(line, "{},"), ", ")
		for i := range fields {
			fields[i] = strings.Trim(fields[i], `"`)
		}
		got = append(got, strings.Join(fields[:3], " "))
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("operations table:\n%v\nwant:\n%v", got, want)
	}
}

func TestOperationTypes(t *testing.T) {
	files, err := generate(contractPath, overlayPath)
	if err != nil {
		t.Fatal(err)
	}
	table := string(files[operationsFile])
	for _, row := range []string{
		`{"createEmail", "POST", "/v1/emails", "CreateEmailRequest", "required", "", "CreateEmailResponse"},`,
		`{"listEmails", "GET", "/v1/emails", "", "", "ListEmailsParams", "EmailList"},`,
		`{"scheduleBroadcast", "POST", "/v1/broadcasts/{id}/schedule", "ScheduleBroadcastRequest", "optional", "", "Broadcast"},`,
		`{"deleteContact", "DELETE", "/v1/contacts/{id}", "", "", "", ""},`,
		`{"getUsage", "GET", "/v1/usage", "", "", "GetUsageParams", "Usage"},`,
		`{"reportUnsubscribe", "POST", "/v1/unsubscribes", "UnsubscribeRequest", "required", "", "UnsubscribeResult"},`,
	} {
		if !strings.Contains(table, row) {
			t.Errorf("table lacks %s", row)
		}
	}
	// Header-only parameters (Idempotency-Key) are not query parameters.
	if strings.Contains(table, "CreateEmailParams") || strings.Contains(table, "CreateContactEventParams") {
		t.Error("header parameters listed as query parameters")
	}
}

func TestOperationTypesMustBeNamed(t *testing.T) {
	dir := t.TempDir()
	overlay := writeFile(t, dir, "overlay.yaml", emptyOverlay)
	cases := map[string]string{
		"inline request body": `  /x:
    post:
      operationId: x
      requestBody: {required: true, content: {application/json: {schema: {type: object}}}}
      responses: {"204": {description: ok}}
`,
		"inline result": `  /x:
    get:
      operationId: x
      responses: {"200": {description: ok, content: {application/json: {schema: {type: object}}}}}
`,
		"two success responses": `  /x:
    get:
      operationId: x
      responses:
        "200": {description: ok, content: {application/json: {schema: {$ref: "#/components/schemas/B"}}}}
        "202": {description: ok}
`,
	}
	for name, path := range cases {
		t.Run(name, func(t *testing.T) {
			contract := writeFile(t, dir, "openapi.yaml", strings.Replace(miniContract, "paths: {}\n", "paths:\n"+path, 1))
			if _, err := generate(contract, overlay); err == nil || !strings.Contains(err.Error(), "x: ") {
				t.Fatalf("generate = %v, want the operation refused", err)
			}
		})
	}
}
