package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"slices"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/oapi-codegen/oapi-codegen/v2/pkg/codegen"
)

// Imports each generated file may use.
var allowedImports = map[string][]string{
	typesFile:      {"encoding/json", "time"},
	operationsFile: {},
}

// generateOperations returns the table of every operation in the contract's
// paths (the webhook is not one), sorted by path and method, with the Go
// types of its request body, query parameters and success result, which the
// SDK's tests check the client against. It runs before codegen.Generate,
// which rewrites operation ids in place.
func generateOperations(doc *openapi3.T) ([]byte, error) {
	var ops []operation
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Value(path)
		for method, o := range item.Operations() {
			if o.OperationID == "" {
				return nil, fmt.Errorf("%s %s has no operationId", method, path)
			}
			op, err := describeOperation(o, item.Parameters, method, path)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", o.OperationID, err)
			}
			ops = append(ops, op)
		}
	}
	slices.SortFunc(ops, func(a, b operation) int {
		if c := strings.Compare(a.path, b.path); c != 0 {
			return c
		}
		return strings.Compare(a.method, b.method)
	})
	var b bytes.Buffer
	b.WriteString(header("tools/oapi-codegen"))
	b.WriteString("package " + packageName + "\n\n")
	b.WriteString(`// operation is one API operation: its operationId, HTTP method and path
// template, and the Go types of its JSON request body (bodyMode "required",
// "optional" or "" for none), query parameters and success result ("" when
// the success response has no body).
type operation struct{ id, method, path, body, bodyMode, params, result string }

`)
	b.WriteString("// operations lists every operation of the API contract.\n")
	b.WriteString("var operations = [...]operation{\n")
	seen := map[string]bool{}
	for _, o := range ops {
		if seen[o.id] {
			return nil, fmt.Errorf("operationId %q is used twice", o.id)
		}
		seen[o.id] = true
		fields := []string{o.id, o.method, o.path, o.body, o.bodyMode, o.params, o.result}
		for i, f := range fields {
			fields[i] = strconv.Quote(f)
		}
		fmt.Fprintf(&b, "\t{%s},\n", strings.Join(fields, ", "))
	}
	b.WriteString("}\n")
	src, err := format.Source(b.Bytes())
	if err != nil {
		return nil, fmt.Errorf("format operations: %w", err)
	}
	return src, nil
}

type operation struct{ id, method, path, body, bodyMode, params, result string }

// describeOperation names the generated Go types an operation uses. Bodies
// and results must be named schemas: inline ones get no generated type.
func describeOperation(o *openapi3.Operation, shared openapi3.Parameters, method, path string) (operation, error) {
	op := operation{id: o.OperationID, method: method, path: path}
	if rb := o.RequestBody; rb != nil {
		name, err := jsonSchemaType(rb.Value.Content)
		if err != nil {
			return op, fmt.Errorf("request body: %w", err)
		}
		op.body = name
		op.bodyMode = "optional"
		if rb.Value.Required {
			op.bodyMode = "required"
		}
	}
	for _, p := range slices.Concat(shared, o.Parameters) {
		if p.Value.In == openapi3.ParameterInQuery {
			op.params = codegen.SchemaNameToTypeName(o.OperationID) + "Params"
			break
		}
	}
	var successes []string
	for code, r := range o.Responses.Map() {
		if len(code) != 3 || code[0] != '2' {
			continue
		}
		successes = append(successes, code)
		if len(r.Value.Content) == 0 {
			continue
		}
		name, err := jsonSchemaType(r.Value.Content)
		if err != nil {
			return op, fmt.Errorf("response %s: %w", code, err)
		}
		op.result = name
	}
	if len(successes) != 1 {
		return op, fmt.Errorf("want exactly one success response, have %v", successes)
	}
	return op, nil
}

func jsonSchemaType(content openapi3.Content) (string, error) {
	media, ok := content["application/json"]
	if !ok || len(content) != 1 || media.Schema == nil {
		return "", errors.New("want exactly one application/json schema")
	}
	ref := media.Schema.Ref
	name, ok := strings.CutPrefix(ref, "#/components/schemas/")
	if !ok || name == "" || strings.Contains(name, "/") {
		return "", errors.New("want a $ref to a component schema")
	}
	return codegen.SchemaNameToTypeName(name), nil
}

// checkGenerated accepts generated source only in the shapes the generator
// produces, so a compromised or misled generator cannot add code that runs:
// imports from allowedImports (no blank, dot or cgo imports), type
// declarations, constants with literal values, Valid methods that only
// compare the receiver with constants, and, in the operations file, one
// table of literals. Anything else, block comments (where code could hide
// from a reader) and compiler or line directives are refused.
func checkGenerated(name string, src []byte) error {
	// Bidirectional controls and zero-width characters make the source a
	// reader sees differ from what compiles; golangci-lint skips generated
	// files, so bidichk does not see them.
	if i := bytes.IndexFunc(src, invisible); i >= 0 {
		return fmt.Errorf("generated %s:%d: invisible character %U", name, bytes.Count(src[:i], []byte("\n"))+1, []rune(string(src[i:]))[0])
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return fmt.Errorf("parse generated %s: %w", name, err)
	}
	c := shapeChecker{fset: fset, name: name}
	if file.Name.Name != packageName {
		return c.refuse(file.Name, "package "+file.Name.Name+", want "+packageName)
	}
	for _, group := range file.Comments {
		for _, comment := range group.List {
			text := comment.Text
			if strings.HasPrefix(text, "/*") {
				return c.refuse(comment, "block comment is not allowed")
			}
			if strings.HasPrefix(text, "//go:") || strings.HasPrefix(text, "//line ") {
				return c.refuse(comment, "directive "+strconv.Quote(strings.Fields(text)[0])+" is not allowed")
			}
		}
	}
	allowed, ok := allowedImports[name]
	if !ok {
		return fmt.Errorf("generated %s: unknown file", name)
	}
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || !slices.Contains(allowed, path) {
			return c.refuse(imp, "import "+imp.Path.Value+" is not allowed")
		}
		if imp.Name != nil {
			return c.refuse(imp, "named import is not allowed")
		}
	}
	if name == operationsFile {
		return c.operations(file)
	}
	return c.types(file)
}

func invisible(r rune) bool {
	return (r >= 0x200B && r <= 0x200F) || (r >= 0x202A && r <= 0x202E) || (r >= 0x2066 && r <= 0x2069) || r == 0xFEFF
}

type shapeChecker struct {
	fset *token.FileSet
	name string
}

func (c shapeChecker) refuse(node ast.Node, what string) error {
	return fmt.Errorf("generated %s:%d: %s", c.name, c.fset.Position(node.Pos()).Line, what)
}

// noCode refuses calls and function literals anywhere inside node.
func (c shapeChecker) noCode(node ast.Node) error {
	var err error
	ast.Inspect(node, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		switch n.(type) {
		case *ast.CallExpr, *ast.FuncLit, *ast.FuncDecl:
			err = c.refuse(n, "code is not allowed here")
		}
		return err == nil
	})
	return err
}

// types checks the declarations of the types file. Every declared name is
// exported (so none shadows a builtin); constants are recorded with their
// type for the Valid methods.
func (c shapeChecker) types(file *ast.File) error {
	types := map[string]bool{}
	consts := map[string]string{}
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		if err := c.typesDecl(d, types, consts); err != nil {
			return err
		}
	}
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
		case *ast.FuncDecl:
			if err := c.validMethod(d, types, consts); err != nil {
				return err
			}
		default:
			return c.refuse(decl, "declaration is not allowed")
		}
	}
	return nil
}

func (c shapeChecker) typesDecl(d *ast.GenDecl, types map[string]bool, consts map[string]string) error {
	if d.Tok == token.IMPORT {
		return nil
	}
	if d.Tok == token.TYPE {
		for _, spec := range d.Specs {
			t, ok := spec.(*ast.TypeSpec)
			if !ok || !ast.IsExported(t.Name.Name) {
				return c.refuse(spec, "type name is not exported")
			}
			if err := c.declarationOnly(t); err != nil {
				return err
			}
			types[t.Name.Name] = true
		}
		return nil
	}
	if d.Tok != token.CONST {
		return c.refuse(d, d.Tok.String()+" declaration is not allowed")
	}
	for _, spec := range d.Specs {
		v, ok := spec.(*ast.ValueSpec)
		if !ok || len(v.Values) != len(v.Names) {
			return c.refuse(spec, "constant without a literal value")
		}
		typ, ok := v.Type.(*ast.Ident)
		if v.Type != nil && !ok {
			return c.refuse(v.Type, "constant type is not a name")
		}
		for i, value := range v.Values {
			if _, ok := value.(*ast.BasicLit); !ok {
				return c.refuse(value, "constant value is not a literal")
			}
			if !ast.IsExported(v.Names[i].Name) {
				return c.refuse(v.Names[i], "constant name is not exported")
			}
			if typ != nil {
				consts[v.Names[i].Name] = typ.Name
			}
		}
	}
	return nil
}

// declarationOnly refuses what could run code or bring in methods: calls,
// function literals, embedded fields and interfaces other than the empty one.
func (c shapeChecker) declarationOnly(node ast.Node) error {
	if err := c.noCode(node); err != nil {
		return err
	}
	var err error
	ast.Inspect(node, func(n ast.Node) bool {
		if err != nil {
			return false
		}
		switch n := n.(type) {
		case *ast.StructType:
			for _, field := range n.Fields.List {
				if len(field.Names) == 0 {
					err = c.refuse(field, "embedded field is not allowed")
				}
			}
		case *ast.InterfaceType:
			if len(n.Methods.List) > 0 {
				err = c.refuse(n, "interface with methods is not allowed")
			}
		}
		return err == nil
	})
	return err
}

// validMethod accepts only the enum check oapi-codegen writes, on a type of
// this file, with constants of that type:
//
//	func (e T) Valid() bool { switch e { case A: return true; …; default: return false } }
func (c shapeChecker) validMethod(fn *ast.FuncDecl, types map[string]bool, consts map[string]string) error {
	refuse := func() error { return c.refuse(fn, "function "+fn.Name.Name+" is not allowed") }
	if fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 || fn.Name.Name != "Valid" ||
		fn.Type.TypeParams != nil || len(fn.Type.Params.List) != 0 ||
		fn.Type.Results == nil || len(fn.Type.Results.List) != 1 || !isIdent(fn.Type.Results.List[0].Type, "bool") {
		return refuse()
	}
	recvType, ok := fn.Recv.List[0].Type.(*ast.Ident)
	if !ok || !types[recvType.Name] {
		return refuse()
	}
	receiver := fn.Recv.List[0].Names[0].Name
	if fn.Body == nil || len(fn.Body.List) != 1 {
		return refuse()
	}
	sw, ok := fn.Body.List[0].(*ast.SwitchStmt)
	if !ok || sw.Init != nil || !isIdent(sw.Tag, receiver) {
		return refuse()
	}
	for _, stmt := range sw.Body.List {
		clause, ok := stmt.(*ast.CaseClause)
		if !ok || len(clause.Body) != 1 {
			return refuse()
		}
		for _, value := range clause.List {
			id, ok := value.(*ast.Ident)
			if !ok || consts[id.Name] != recvType.Name {
				return refuse()
			}
		}
		want := "true"
		if clause.List == nil {
			want = "false"
		}
		ret, ok := clause.Body[0].(*ast.ReturnStmt)
		if !ok || len(ret.Results) != 1 || !isIdent(ret.Results[0], want) {
			return refuse()
		}
	}
	return nil
}

// operations accepts exactly the operation type and the table of literals.
func (c shapeChecker) operations(file *ast.File) error {
	var sawType, sawTable bool
	for _, decl := range file.Decls {
		d, ok := decl.(*ast.GenDecl)
		if !ok || len(d.Specs) != 1 {
			return c.refuse(decl, "declaration is not allowed")
		}
		switch spec := d.Specs[0].(type) {
		case *ast.TypeSpec:
			if sawType || spec.Name.Name != "operation" {
				return c.refuse(spec, "type is not allowed")
			}
			if err := c.declarationOnly(spec); err != nil {
				return err
			}
			sawType = true
		case *ast.ValueSpec:
			if sawTable || d.Tok != token.VAR || len(spec.Names) != 1 || spec.Names[0].Name != "operations" || len(spec.Values) != 1 {
				return c.refuse(spec, "declaration is not allowed")
			}
			table, ok := spec.Values[0].(*ast.CompositeLit)
			if !ok || spec.Type != nil || !isOperationArray(table.Type) {
				return c.refuse(spec, "operations is not a [...]operation table")
			}
			for _, elt := range table.Elts {
				row, ok := elt.(*ast.CompositeLit)
				if !ok || row.Type != nil {
					return c.refuse(elt, "row is not a literal")
				}
				for _, field := range row.Elts {
					if _, ok := field.(*ast.BasicLit); !ok {
						return c.refuse(field, "field is not a literal")
					}
				}
			}
			sawTable = true
		default:
			return c.refuse(decl, "declaration is not allowed")
		}
	}
	return nil
}

func isOperationArray(expr ast.Expr) bool {
	array, ok := expr.(*ast.ArrayType)
	if !ok || !isIdent(array.Elt, "operation") {
		return false
	}
	_, ellipsis := array.Len.(*ast.Ellipsis)
	return ellipsis
}

func isIdent(expr ast.Expr, name string) bool {
	id, ok := expr.(*ast.Ident)
	return ok && id.Name == name
}
