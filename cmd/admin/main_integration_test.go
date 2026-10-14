//go:build integration

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/skraus2/email-platform/internal/testdb"
)

func admin(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, &out, &errOut)
	return out.String(), err
}

func TestSenderCommands(t *testing.T) {
	dbURL, _ := testdb.NewMigratedURL(t)
	t.Setenv("DATABASE_URL", dbURL)

	out, err := admin(t, "project", "create", "-name", "app")
	if err != nil {
		t.Fatal(err)
	}
	project := strings.Fields(out)[0]

	out, err = admin(t, "sender", "add", "-project", project, "-domain", "Example.test")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(out)
	if len(fields) != 4 || fields[1] != "domain" || fields[2] != "example.test" || fields[3] != "pending" {
		t.Fatalf("add output = %q", out)
	}
	id := fields[0]

	for name, args := range map[string][]string{
		"both kinds":     {"sender", "add", "-project", project, "-domain", "a.test", "-address", "x@a.test"},
		"no kind":        {"sender", "add", "-project", project},
		"bad project":    {"sender", "add", "-project", "nope", "-domain", "a.test"},
		"duplicate":      {"sender", "add", "-project", project, "-domain", "EXAMPLE.test"},
		"bad id":         {"sender", "verify", "-id", "nope"},
		"unknown remove": {"sender", "remove", "-id", "00000000-0000-0000-0000-000000000000"},
	} {
		if _, err := admin(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := admin(t, "sender", "verify", "-id", id); err != nil {
		t.Fatal(err)
	}
	if _, err := admin(t, "sender", "verify", "-id", id); err == nil {
		t.Error("second verify accepted; want an error for an already verified identity")
	}
	out, err = admin(t, "sender", "list", "-project", project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, "verified") {
		t.Fatalf("list = %q", out)
	}
	if _, err := admin(t, "sender", "remove", "-id", id); err != nil {
		t.Fatal(err)
	}
	if out, err := admin(t, "sender", "list", "-project", project); err != nil || strings.Contains(out, id) {
		t.Fatalf("list after remove = %q, %v", out, err)
	}
}
