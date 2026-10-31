package emailplatform

import (
	"regexp"
	"strings"
	"testing"
)

func TestOperationsTable(t *testing.T) {
	if len(operations) != 37 {
		t.Fatalf("%d operations, want 37", len(operations))
	}
	ids := map[string]bool{}
	routes := map[string]bool{}
	id := regexp.MustCompile(`^[a-z][A-Za-z]+$`)
	path := regexp.MustCompile(`^/v1(/[a-z-]+|/\{[a-z_]+\})+$`)
	for _, op := range operations {
		if !id.MatchString(op.id) || ids[op.id] {
			t.Errorf("operation id %q is malformed or repeated", op.id)
		}
		ids[op.id] = true
		switch op.method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			t.Errorf("%s: method %q", op.id, op.method)
		}
		if !path.MatchString(op.path) || strings.Contains(op.path, "//") {
			t.Errorf("%s: path %q", op.id, op.path)
		}
		route := op.method + " " + op.path
		if routes[route] {
			t.Errorf("%s: route %s repeated", op.id, route)
		}
		routes[route] = true
	}
}
