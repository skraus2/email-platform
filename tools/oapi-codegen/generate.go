// Command oapi-codegen generates the Go SDK's types and its operation table
// from the API contract and the SDK's overlay.
//
//	go -C tools/oapi-codegen run . -contract C -overlay O -out DIR          write the files
//	go -C tools/oapi-codegen run . -contract C -overlay O -out DIR -check   fail if they differ
//
// oapi-codegen is used as a library rather than through its command line,
// because the command line reads every local file a $ref names. Here the
// contract and the overlay are first checked for anything that would make
// the loader read another document (again after the overlay is applied),
// the loader is then given the checked bytes and no way to read anything
// else, and the generated source is checked before it is written: it may
// hold only declarations in the shapes the generator writes (see
// checkGenerated), so nothing in it can run code of its own.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
	"github.com/speakeasy-api/openapi/overlay"
	"gopkg.in/yaml.v3"
)

const (
	packageName    = "emailplatform"
	typesFile      = "types.gen.go"
	operationsFile = "operations.gen.go"
	regenerate     = "make sdk-go-generate"
)

func main() {
	contract := flag.String("contract", "", "path of the OpenAPI contract")
	overlayPath := flag.String("overlay", "", "path of the OpenAPI overlay")
	out := flag.String("out", "", "directory the generated files belong in")
	check := flag.Bool("check", false, "fail if the files in -out differ instead of writing them")
	flag.Parse()
	if *contract == "" || *overlayPath == "" || *out == "" || flag.NArg() != 0 {
		flag.Usage()
		os.Exit(2)
	}
	if err := run(*contract, *overlayPath, *out, *check); err != nil {
		fmt.Fprintln(os.Stderr, "sdk-go generate:", err)
		os.Exit(1)
	}
}

func run(contract, overlayPath, out string, check bool) error {
	files, err := generate(contract, overlayPath)
	if err != nil {
		return err
	}
	if check {
		return checkDrift(files, out)
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := os.WriteFile(filepath.Join(out, name), files[name], 0o644); err != nil { //nolint:gosec // generated source is meant to be readable
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// generate returns the generated files by name.
func generate(contractPath, overlayPath string) (map[string][]byte, error) {
	contract, err := os.ReadFile(contractPath) //nolint:gosec // path comes from the Makefile
	if err != nil {
		return nil, fmt.Errorf("read contract: %w", err)
	}
	overlayData, err := os.ReadFile(overlayPath) //nolint:gosec // path comes from the Makefile
	if err != nil {
		return nil, fmt.Errorf("read overlay: %w", err)
	}
	if reason, err := nonLocalInContract(contract); err != nil {
		return nil, fmt.Errorf("%s: %w", contractPath, err)
	} else if reason != "" {
		return nil, fmt.Errorf(`%s: %s; only "#/..." references are allowed`, contractPath, reason)
	}
	if reason, err := nonLocalInOverlay(overlayData); err != nil {
		return nil, fmt.Errorf("%s: %w", overlayPath, err)
	} else if reason != "" {
		return nil, fmt.Errorf(`%s: %s; only "#/..." references are allowed`, overlayPath, reason)
	}
	doc, err := load(contractPath, contract)
	if err != nil {
		return nil, err
	}
	if doc, err = applyOverlay(contractPath, doc, overlayData); err != nil {
		return nil, err
	}
	// Before the types: codegen.Generate rewrites operation ids in place.
	operations, err := generateOperations(doc)
	if err != nil {
		return nil, err
	}
	types, err := generateTypes(doc)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{typesFile: types, operationsFile: operations}
	for name, src := range files {
		if err := checkGenerated(name, src); err != nil {
			return nil, err
		}
	}
	return files, nil
}

// load parses an OpenAPI document with a loader that cannot read files or
// URLs: any reference to another document fails instead of being followed.
func load(path string, data []byte) (*openapi3.T, error) {
	return loadWith(path, data, refuseRead)
}

func loadWith(path string, data []byte, read openapi3.ReadFromURIFunc) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = false
	// Without a reader of its own kin-openapi falls back to reading files and
	// URLs, e.g. it re-reads the document from disk for a local reference it
	// cannot resolve in memory.
	loader.ReadFromURIFunc = read
	loader.IncludeOrigin = true
	doc, err := loader.LoadFromDataWithPath(data, fileLocation(path))
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", path, err)
	}
	return doc, nil
}

// applyOverlay applies the overlay in strict mode (an action whose target is
// gone fails) and loads the result with the same reader-less loader.
func applyOverlay(path string, doc *openapi3.T, data []byte) (*openapi3.T, error) {
	ov, err := overlay.ParseReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("parse overlay: %w", err)
	}
	if err := ov.Validate(); err != nil {
		return nil, fmt.Errorf("invalid overlay: %w", err)
	}
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("encode contract: %w", err)
	}
	var node yaml.Node
	if err := yaml.NewDecoder(&buf).Decode(&node); err != nil {
		return nil, fmt.Errorf("decode contract: %w", err)
	}
	if warnings, err := ov.ApplyToStrict(&node); err != nil {
		return nil, fmt.Errorf("apply overlay: %w (%s)", err, strings.Join(warnings, "; "))
	}
	out, err := yaml.Marshal(&node)
	if err != nil {
		return nil, fmt.Errorf("encode overlaid contract: %w", err)
	}
	// Overlay values are judged where they end up, e.g. inside a
	// discriminator, so the result is walked again before it is loaded.
	if reason, err := nonLocalInContract(out); err != nil {
		return nil, fmt.Errorf("overlaid contract: %w", err)
	} else if reason != "" {
		return nil, fmt.Errorf(`overlaid contract: %s; only "#/..." references are allowed`, reason)
	}
	return load(path, out)
}

func refuseRead(_ *openapi3.Loader, location *url.URL) ([]byte, error) {
	return nil, fmt.Errorf("refusing to read %s while generating", location.Redacted())
}

func generateTypes(doc *openapi3.T) ([]byte, error) {
	cfg := codegen.Configuration{
		PackageName: packageName,
		Generate:    codegen.GenerateOptions{Models: true},
		OutputOptions: codegen.OutputOptions{
			// The webhook payload types are used only by the 3.1 webhooks
			// section, which pruning does not see.
			SkipPrune: true,
			TypeMapping: &codegen.TypeMapping{String: codegen.FormatMapping{
				Formats: map[string]codegen.SimpleTypeSpec{
					"uuid":  {Type: "string"},
					"email": {Type: "string"},
					"date":  {Type: "string"},
				},
			}},
		},
		AllowRemoteRefsFrom: []string{},
	}
	cfg = cfg.UpdateDefaults()
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("generator configuration: %w", err)
	}
	code, err := codegen.Generate(doc, cfg)
	if err != nil {
		return nil, fmt.Errorf("generate types: %w", err)
	}
	return withHeader(code)
}

// withHeader replaces the generator's header, which names the program that
// ran it and its version, so the output does not depend on how it was run.
func withHeader(code string) ([]byte, error) {
	start := strings.Index(code, "\npackage ")
	if !strings.HasPrefix(code, "package ") && start < 0 {
		return nil, errors.New("generated types have no package clause")
	}
	return []byte(header("oapi-codegen") + code[start+1:]), nil
}

func header(by string) string {
	return "// Code generated from api/openapi.yaml by " + by + "; DO NOT EDIT.\n" +
		"// Regenerate with `" + regenerate + "`.\n\n"
}

func checkDrift(files map[string][]byte, dir string) error {
	var stale []string
	for _, name := range slices.Sorted(maps.Keys(files)) {
		path := filepath.Join(dir, name)
		committed, err := os.ReadFile(path) //nolint:gosec // path is the SDK directory from the Makefile
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read %s: %w", path, err)
		}
		if !bytes.Equal(committed, files[name]) {
			stale = append(stale, path)
		}
	}
	if len(stale) > 0 {
		return fmt.Errorf("%s out of date with the contract; run: %s", strings.Join(stale, ", "), regenerate)
	}
	return nil
}

// fileLocation is the URL the loader resolves relative references against;
// with external references refused it only names the document in errors.
func fileLocation(path string) *url.URL {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return &url.URL{Path: filepath.ToSlash(path)}
}
