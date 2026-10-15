//go:build integration

package store

import (
	"context"
	"strings"
	"testing"

	"github.com/skraus2/email-platform/internal/testdb"
)

// Prepared statements may run with a generic plan. The list queries must
// still bound their index scans by the cursor (and the recipient), so deep
// pages and recipient filters never re-scan a whole project.
func TestEmailListQueriesUseIndexesInGenericPlans(t *testing.T) {
	t.Parallel()
	pool := testdb.NewMigrated(t)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	// Realistic statistics: one project, many recipients.
	if _, err := conn.Exec(ctx, `
		WITH p AS (INSERT INTO projects (name) VALUES ('plan') RETURNING id)
		INSERT INTO emails (project_id, sender, recipient, subject)
		SELECT p.id, 'noreply@example.test', 'user' || g || '@example.test', 'Hi'
		FROM p, generate_series(1, 5000) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "ANALYZE emails"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, "SET enable_seqscan = off"); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		query string
		want  []string
	}{
		"list":         {listProjectEmails, []string{"Index Scan", "(id < $2)", "(id >= $3)"}},
		"by recipient": {listProjectEmailsByRecipient, []string{"emails_project_recipient_idx", "lower(recipient)", "(id < $3)", "(id >= $4)"}},
	} {
		// The raw simple protocol leaves the $n placeholders to the server.
		results, err := conn.Conn().PgConn().Exec(ctx, "EXPLAIN (GENERIC_PLAN) "+tc.query).ReadAll()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan strings.Builder
		for _, row := range results[0].Rows {
			plan.Write(row[0])
			plan.WriteString("\n")
		}
		var cond string
		for _, l := range strings.Split(plan.String(), "\n") {
			if strings.Contains(l, "Index Cond") {
				cond += l
			}
		}
		if !strings.Contains(plan.String(), tc.want[0]) {
			t.Errorf("%s: plan does not use %s:\n%s", name, tc.want[0], plan.String())
		}
		for _, w := range tc.want[1:] {
			if !strings.Contains(cond, w) {
				t.Errorf("%s: index condition lacks %q:\n%s", name, w, plan.String())
			}
		}
	}
}
