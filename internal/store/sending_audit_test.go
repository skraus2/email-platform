package store

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// keepsSending are the only queries that may write updated_at of an email
// that is "sending" afterwards: the claim and the attempt error. The time
// since the last attempt of an email left sending is measured as
// now() − updated_at, so any other such write would shorten the wait before
// a resend.
var keepsSending = []string{"MarkSending", "RecordAttemptError"}

// leavesSendingByParam set the status from a parameter; the caller passes a
// final status (the provider's captured or submitted).
var leavesSendingByParam = map[string]string{
	"MarkDispatched": "status is the provider result: captured or submitted",
}

var (
	updateEmails = regexp.MustCompile(`(?is)\bupdate\s+emails\b`)
	// SET status = 'literal'
	setStatus = regexp.MustCompile(`(?is)\bset\s+status\s*=\s*'(\w+)'`)
	// WHERE … status = 'x' / status IN ('x', 'y') on the updated row (bare
	// or emails.status; another table's status scopes nothing here)
	whereStatus = regexp.MustCompile(`(?is)(?:^|[^.\w])(?:emails\.)?status\s+(?:=\s*'(\w+)'|in\s*\(([^)]*)\))`)
)

// TestOnlyAttemptsTouchSendingEmails checks every query in db/queries that
// updates emails: it either is the claim or the attempt error, or it can
// never leave a row "sending" (sets another status, or only matches rows in
// other statuses).
func TestOnlyAttemptsTouchSendingEmails(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "db", "queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	seen := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		names := queryName.FindAllSubmatchIndex(raw, -1)
		for i, m := range names {
			name := string(raw[m[2]:m[3]])
			end := len(raw)
			if i+1 < len(names) {
				end = names[i+1][0]
			}
			body := comment.ReplaceAllString(string(raw[m[1]:end]), "")
			if !updateEmails.MatchString(body) {
				continue
			}
			seen++
			if slices.Contains(keepsSending, name) {
				continue
			}
			if _, ok := leavesSendingByParam[name]; ok {
				continue
			}
			if !leavesOrSkipsSending(body) {
				t.Errorf("%s (%s) can write updated_at of an email that stays sending; only %v may", name, filepath.Base(f), keepsSending)
			}
		}
	}
	if seen < len(keepsSending)+len(leavesSendingByParam)+3 {
		t.Fatalf("only %d email updates found; parsing broke?", seen)
	}
}

// leavesOrSkipsSending: SET status to a literal other than sending, or every
// status filter of the statement excludes sending.
func leavesOrSkipsSending(body string) bool {
	if m := setStatus.FindStringSubmatch(body); m != nil {
		return m[1] != "sending"
	}
	where := body
	if i := strings.Index(strings.ToLower(body), "where"); i >= 0 {
		where = body[i:]
	}
	filters := whereStatus.FindAllStringSubmatch(where, -1)
	if len(filters) == 0 {
		return false
	}
	for _, f := range filters {
		if f[1] == "sending" || strings.Contains(f[2], "'sending'") {
			return false
		}
	}
	return true
}

func TestLeavesOrSkipsSending(t *testing.T) {
	for body, want := range map[string]bool{
		"UPDATE emails SET status = 'failed', updated_at = now() WHERE id = $1 AND status IN ('queued', 'sending')":          true,
		"UPDATE emails SET last_error = $3, updated_at = now() WHERE id = $1 AND status = 'sending'":                         false,
		"UPDATE emails SET updated_at = now() WHERE id = $1":                                                                 false,
		"UPDATE emails SET x = 1, updated_at = now() WHERE id = $1 AND status IN ('submitted', 'delivered')":                 true,
		"UPDATE emails SET status = 'sending', updated_at = now() WHERE id = $1":                                             false,
		"UPDATE emails SET updated_at = now() WHERE id = $1 AND EXISTS (SELECT 1 FROM projects p WHERE p.status = 'active')": false,
		"UPDATE emails SET updated_at = now() WHERE emails.id = $1 AND emails.status = 'queued'":                             true,
	} {
		if got := leavesOrSkipsSending(body); got != want {
			t.Errorf("%q = %v, want %v", body, got, want)
		}
	}
}
