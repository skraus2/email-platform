//go:build integration

package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/testdb"
)

// seedPlanData gives the planner realistic statistics: several projects,
// many recipients.
func seedPlanData(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `
		WITH p AS (INSERT INTO projects (name) SELECT 'plan-' || n FROM generate_series(1, 10) n RETURNING id)
		INSERT INTO emails (project_id, sender, recipient, subject)
		SELECT p.id, 'noreply@example.test', 'user' || g || '@example.test', 'Hi'
		FROM p, generate_series(1, 500) g`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE emails"); err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	if err := pool.QueryRow(ctx, "SELECT id FROM projects WHERE name = 'plan-3'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedTemplateSends gives one rarely used template a few emails of the
// project and returns its id.
func seedTemplateSends(t *testing.T, pool *pgxpool.Pool, project uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var id uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO templates (project_id, slug, latest_version) VALUES ($1, 'rare', 1) RETURNING id`, project).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO template_versions (template_id, project_id, version, subject, text_body) VALUES ($1, $2, 1, 's', 't')`, id, project); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version)
		SELECT $1, 'noreply@example.test', 'x@example.test', 'Hi', $2, 1 FROM generate_series(1, 3)`, project, id); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "ANALYZE emails"); err != nil {
		t.Fatal(err)
	}
	return id
}

// The list queries run as unnamed statements (Unprepared), so every
// execution is planned with its values: the first page and a recipient
// filter scan only the project's own index range.
func TestEmailListQueriesPlanWithActualValues(t *testing.T) {
	t.Parallel()
	pool := testdb.NewMigrated(t)
	project := seedPlanData(t, pool)
	ctx := context.Background()
	var after *time.Time
	var noTemplate, noBroadcast *uuid.UUID
	someBroadcast := uuid.New()
	tmpl := seedTemplateSends(t, pool, project)
	for name, tc := range map[string]struct {
		query string
		args  []any
		want  []string
	}{
		"first page": {listProjectEmails,
			[]any{project, uuid.Max, uuid.Nil, []string{}, after, after, noTemplate, noBroadcast, int32(51)},
			[]string{"emails_project_id_idx", "(project_id = "}},
		"by template": {listProjectEmails,
			[]any{project, uuid.Max, uuid.Nil, []string{}, after, after, &tmpl, noBroadcast, int32(51)},
			[]string{"emails_project_template_idx", "(template_id = "}},
		"by broadcast": {listProjectEmails,
			[]any{project, uuid.Max, uuid.Nil, []string{}, after, after, noTemplate, &someBroadcast, int32(51)},
			[]string{"emails_broadcast_idx", "(broadcast_id = "}},
		"by recipient": {listProjectEmailsByRecipient,
			[]any{project, "USER7@example.test", uuid.Max, uuid.Nil, []string{}, after, after, noTemplate, noBroadcast, int32(51)},
			[]string{"emails_project_recipient_idx", "lower(recipient)"}},
	} {
		rows, err := pool.Query(ctx, "EXPLAIN "+tc.query, append([]any{pgx.QueryExecModeDescribeExec}, tc.args...)...)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var plan strings.Builder
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(line + "\n")
		}
		rows.Close()
		for _, w := range tc.want {
			if !strings.Contains(plan.String(), w) {
				t.Errorf("%s: plan lacks %q:\n%s", name, w, plan.String())
			}
		}
	}
}

// Unprepared never leaves a named prepared statement (with its cached,
// possibly generic plan) on the connection; the default mode does.
func TestUnpreparedDoesNotCacheStatements(t *testing.T) {
	t.Parallel()
	pool := testdb.NewMigrated(t)
	project := seedPlanData(t, pool)
	ctx := context.Background()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	params := ListProjectEmailsParams{ProjectID: project, BeforeID: uuid.Max, Statuses: []string{}, RowLimit: 10}
	prepared := func() int {
		var n int
		if err := conn.QueryRow(ctx, "SELECT count(*) FROM pg_prepared_statements WHERE statement LIKE '-- name: ListProjectEmails %'",
			pgx.QueryExecModeSimpleProtocol).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for range 7 {
		if _, err := New(Unprepared(conn)).ListProjectEmails(ctx, params); err != nil {
			t.Fatal(err)
		}
	}
	if n := prepared(); n != 0 {
		t.Fatalf("Unprepared left %d prepared statements", n)
	}
	if _, err := New(conn).ListProjectEmails(ctx, params); err != nil {
		t.Fatal(err)
	}
	if n := prepared(); n != 1 {
		t.Fatalf("control: default mode left %d prepared statements, want 1", n)
	}
}

// The server itself cancels a statement past the timeout.
func TestReadOnlyWithTimeoutCancelsOnTheServer(t *testing.T) {
	t.Parallel()
	pool := testdb.NewMigrated(t)
	ctx := context.Background()
	start := time.Now()
	err := ReadOnlyWithTimeout(ctx, pool, 200*time.Millisecond, func(q *Queries) error {
		_, err := q.db.Exec(ctx, "SELECT pg_sleep(5)")
		return err
	})
	if !StatementTimedOut(err) || time.Since(start) > 3*time.Second {
		t.Fatalf("err %v after %v, want a server-side statement timeout", err, time.Since(start))
	}
	// Read-only.
	err = ReadOnlyWithTimeout(ctx, pool, time.Second, func(q *Queries) error {
		_, err := q.db.Exec(ctx, "INSERT INTO projects (name) VALUES ('x')")
		return err
	})
	if err == nil {
		t.Fatal("write accepted in a read-only transaction")
	}
}
