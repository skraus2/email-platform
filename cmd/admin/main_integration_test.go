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
	err := run(context.Background(), args, strings.NewReader(""), &out, &errOut)
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
		"bad id":         {"sender", "verify", "-project", project, "-id", "nope"},
		"unknown remove": {"sender", "remove", "-project", project, "-id", "00000000-0000-0000-0000-000000000000"},
		"no project":     {"sender", "verify", "-id", "00000000-0000-0000-0000-000000000000"},
	} {
		if _, err := admin(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	if _, err := admin(t, "sender", "verify", "-project", project, "-id", id); err != nil {
		t.Fatal(err)
	}
	if _, err := admin(t, "sender", "verify", "-project", project, "-id", id); err == nil {
		t.Error("second verify accepted; want an error for an already verified identity")
	}
	out, err = admin(t, "sender", "list", "-project", project)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, id) || !strings.Contains(out, "verified") {
		t.Fatalf("list = %q", out)
	}
	if _, err := admin(t, "sender", "remove", "-project", project, "-id", id); err != nil {
		t.Fatal(err)
	}
	if out, err := admin(t, "sender", "list", "-project", project); err != nil || strings.Contains(out, id) {
		t.Fatalf("list after remove = %q, %v", out, err)
	}
}

func TestWebhookCommands(t *testing.T) {
	dbURL, _ := testdb.NewMigratedURL(t)
	t.Setenv("DATABASE_URL", dbURL)
	t.Setenv("WEBHOOK_KEKS", "k1:"+strings.Repeat("A", 43)+"=")

	out, err := admin(t, "project", "create", "-name", "app")
	if err != nil {
		t.Fatal(err)
	}
	project := strings.Fields(out)[0]

	out, err = admin(t, "webhook", "create", "-project", project, "-url", "http://127.0.0.1:9/hook?token=s3cret", "-events", "email.bounced,email.complained")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], "email.bounced,email.complained") || !strings.Contains(lines[1], "whsec_") {
		t.Fatalf("create output = %q", out)
	}
	id := strings.Fields(lines[0])[0]
	secret := lines[1][strings.Index(lines[1], "whsec_"):]

	out, err = admin(t, "webhook", "list", "-project", project)
	if err != nil || !strings.Contains(out, id) || !strings.Contains(out, "active") || strings.Contains(out, secret) ||
		!strings.Contains(out, "127.0.0.1:9/hook") || !strings.Contains(out, "email.bounced,email.complained") {
		t.Fatalf("list = %q, %v (must not show the secret)", out, err)
	}
	if _, err := admin(t, "webhook", "disable", "-project", project, "-id", id); err != nil {
		t.Fatal(err)
	}
	if out, _ := admin(t, "webhook", "list", "-project", project); !strings.Contains(out, "disabled") {
		t.Fatalf("after disable: %q", out)
	}
	out, err = admin(t, "webhook", "rotate-secret", "-project", project, "-id", id)
	if err != nil || !strings.Contains(out, "whsec_") || strings.Contains(out, secret) {
		t.Fatalf("rotate = %q, %v", out, err)
	}
	if out, err := admin(t, "webhook", "reencrypt"); err != nil || !strings.Contains(out, "re-encrypted 0") {
		t.Fatalf("reencrypt = %q, %v", out, err)
	}
	for name, args := range map[string][]string{
		"unknown event": {"webhook", "create", "-project", project, "-url", "https://h.example.test/", "-events", "email.opened"},
		"metadata url":  {"webhook", "create", "-project", project, "-url", "https://169.254.169.254/"},
		"bad id":        {"webhook", "enable", "-project", project, "-id", "nope"},
		"foreign id":    {"webhook", "enable", "-project", project, "-id", "00000000-0000-0000-0000-000000000000"},
	} {
		if _, err := admin(t, args...); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	out, err = admin(t, "webhook", "deliveries", "-project", project, "-status", "failed")
	if err != nil || !strings.HasPrefix(out, "ID") {
		t.Fatalf("deliveries = %q, %v", out, err)
	}
	if _, err := admin(t, "webhook", "deliveries", "-project", project, "-status", "lost"); err == nil {
		t.Error("unknown delivery status accepted")
	}

	t.Setenv("WEBHOOK_KEKS", "")
	if _, err := admin(t, "webhook", "create", "-project", project, "-url", "https://h.example.test/"); err == nil || !strings.Contains(err.Error(), "WEBHOOK_KEKS") {
		t.Fatalf("without keys: err = %v", err)
	}
}

func adminIn(t *testing.T, stdin string, args ...string) (string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	err := run(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return out.String(), err
}

func TestUserCommands(t *testing.T) {
	dbURL, _ := testdb.NewMigratedURL(t)
	t.Setenv("DATABASE_URL", dbURL)

	if _, err := adminIn(t, "correct horse battery\n", "user", "create", "-username", "alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := adminIn(t, "short\n", "user", "create", "-username", "bob"); err == nil {
		t.Fatal("short password accepted")
	}
	if _, err := adminIn(t, "correct horse battery\n", "user", "create", "-username", "alice"); err == nil {
		t.Fatal("duplicate user accepted")
	}
	if _, err := adminIn(t, "", "user", "create"); err == nil {
		t.Fatal("missing -username accepted")
	}
	out, err := admin(t, "user", "list")
	if err != nil || !strings.Contains(out, "alice\tenabled") || strings.Contains(out, "correct horse") {
		t.Fatalf("list: %q %v", out, err)
	}
	for _, cmd := range []string{"disable", "enable", "unlock"} {
		if _, err := admin(t, "user", cmd, "-username", "alice"); err != nil {
			t.Fatalf("%s: %v", cmd, err)
		}
		if _, err := admin(t, "user", cmd, "-username", "nobody"); err == nil {
			t.Fatalf("%s of an unknown user succeeded", cmd)
		}
	}
	if _, err := admin(t, "user", "disable", "-username", "alice"); err != nil {
		t.Fatal(err)
	}
	if out, _ := admin(t, "user", "list"); !strings.Contains(out, "alice\tdisabled") {
		t.Fatalf("list after disable: %q", out)
	}
	if _, err := adminIn(t, "another long password\r\n", "user", "set-password", "-username", "alice"); err != nil {
		t.Fatal(err)
	}
}
