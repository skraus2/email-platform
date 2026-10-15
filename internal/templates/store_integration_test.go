//go:build integration

package templates_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skraus2/email-platform/internal/auth"
	"github.com/skraus2/email-platform/internal/store"
	"github.com/skraus2/email-platform/internal/templates"
	"github.com/skraus2/email-platform/internal/testdb"
)

func setup(t *testing.T) (*pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testdb.NewMigrated(t)
	q := store.New(pool)
	a, err := auth.CreateProject(context.Background(), q, "a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.CreateProject(context.Background(), q, "b")
	if err != nil {
		t.Fatal(err)
	}
	return pool, a.ID, b.ID
}

var src = templates.Source{Subject: "Hi {{.name}}", Text: "Hello {{.name}}", HTML: "<p>Hello {{.name}}</p>"}

const schema = `{"name":{"type":"string","required":true}}`

func TestCreateTemplate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := templates.NewStore(pool, 200, 500, 1<<20)

	tmpl, err := s.Create(ctx, a, "welcome", "Welcome mail")
	if err != nil {
		t.Fatal(err)
	}
	if tmpl.Slug != "welcome" || tmpl.Name != "Welcome mail" || tmpl.LatestVersion != 0 || tmpl.ProjectID != a {
		t.Fatalf("created %+v", tmpl)
	}
	if _, err := s.Create(ctx, a, "welcome", ""); !errors.Is(err, templates.ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	if _, err := s.Create(ctx, b, "welcome", ""); err != nil {
		t.Fatalf("same slug in another project: %v", err)
	}
	for _, bad := range []string{"", "Welcome", "-x", "a_b", strings.Repeat("a", 64)} {
		var te *templates.Error
		if _, err := s.Create(ctx, a, bad, ""); !errors.As(err, &te) || te.Field != "slug" {
			t.Errorf("slug %q: %v", bad, err)
		}
	}
	for _, bad := range []string{strings.Repeat("n", 201), "a\x00b", "line\nbreak", "rtl\u202eexe.txt", "\xff"} {
		var te *templates.Error
		if _, err := s.Create(ctx, a, "n"+uuid.NewString()[:8], bad); !errors.As(err, &te) || te.Field != "name" {
			t.Errorf("name %q: %v", bad, err)
		}
	}
	if _, err := s.Create(ctx, a, "long-name", strings.Repeat("ü", 200)); err != nil {
		t.Errorf("200-character name: %v", err)
	}
	if _, err := s.Create(ctx, uuid.New(), "x", ""); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown project: %v", err)
	}
	// Foreign ids are not found.
	if _, err := s.Get(ctx, b, tmpl.ID); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("foreign get: %v", err)
	}
}

func TestTemplateCapIsRaceSafe(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, _ := setup(t)
	s := templates.NewStore(pool, 3, 500, 1<<20)
	for i := range 2 {
		if _, err := s.Create(ctx, a, fmt.Sprintf("t%d", i), ""); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	errs := make([]error, 10)
	for i := range 10 {
		wg.Go(func() { _, errs[i] = s.Create(ctx, a, fmt.Sprintf("c%d", i), "") })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, templates.ErrTemplateLimit):
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 1 {
		t.Fatalf("%d creates succeeded at limit-1, want 1", ok)
	}
}

func TestPublishConcurrentVersionsAreGapless(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, _ := setup(t)
	s := templates.NewStore(pool, 200, 500, 1<<20)
	tmpl, err := s.Create(ctx, a, "welcome", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	versions := make([]int, 10)
	for i := range 10 {
		wg.Go(func() {
			v, err := s.Publish(ctx, a, tmpl.ID, src, []byte(schema))
			if err != nil {
				t.Error(err)
				return
			}
			versions[i] = v.Version
		})
	}
	wg.Wait()
	slices.Sort(versions)
	if !slices.Equal(versions, []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}) {
		t.Fatalf("versions %v", versions)
	}
	got, err := s.Get(ctx, a, tmpl.ID)
	if err != nil || got.LatestVersion != 10 {
		t.Fatalf("latest %d %v", got.LatestVersion, err)
	}
}

func TestPublishLimitsAndErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := templates.NewStore(pool, 200, 2, 1<<20)
	tmpl, err := s.Create(ctx, a, "welcome", "")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := s.Publish(ctx, a, tmpl.ID, src, []byte(schema)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Publish(ctx, a, tmpl.ID, src, []byte(schema)); !errors.Is(err, templates.ErrVersionLimit) {
		t.Fatalf("over the cap: %v", err)
	}
	// A foreign or unknown template is not found and gets no version.
	if _, err := s.Publish(ctx, b, tmpl.ID, src, []byte(schema)); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("foreign publish: %v", err)
	}
	if _, err := s.Publish(ctx, a, uuid.New(), src, []byte(schema)); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("unknown publish: %v", err)
	}
	// Invalid sources or schemas are refused before anything is stored.
	var te *templates.Error
	if _, err := s.Publish(ctx, a, tmpl.ID, templates.Source{Subject: "x", Text: "{{.other}}"}, nil); !errors.As(err, &te) || te.Code != templates.CodeTemplateInvalid {
		t.Fatalf("invalid source: %v", err)
	}
	if _, err := s.Publish(ctx, a, tmpl.ID, src, []byte(`{"name":{"type":"date"}}`)); !errors.As(err, &te) || te.Code != templates.CodeValidation {
		t.Fatalf("invalid schema: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM template_versions").Scan(&n); err != nil || n != 2 {
		t.Fatalf("%d versions stored, want 2 (%v)", n, err)
	}
}

func TestVersionsAreImmutableAndRetrievableUnchanged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := templates.NewStore(pool, 200, 500, 1<<20)
	tmpl, err := s.Create(ctx, a, "welcome", "")
	if err != nil {
		t.Fatal(err)
	}
	v1src := templates.Source{Subject: "  Hi {{ .name }}\t", Text: "Hello {{.name}}\r\n\n  ünïcode ✓", HTML: "<p>\n  {{.name}}\n</p>"}
	if _, err := s.Publish(ctx, a, tmpl.ID, v1src, []byte(`{ "name" : { "required":true, "type":"string" } }`)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, a, tmpl.ID, templates.Source{Subject: "v2", Text: "v2"}, nil); err != nil {
		t.Fatal(err)
	}
	v1, err := s.GetVersion(ctx, a, tmpl.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if v1.Source != v1src {
		t.Fatalf("version 1 changed:\n got %+v\nwant %+v", v1.Source, v1src)
	}
	if string(v1.Schema) != `{"name": {"type": "string", "required": true, "max_length": 1000}}` &&
		string(v1.Schema) != `{"name":{"type":"string","required":true,"max_length":1000}}` {
		t.Fatalf("schema %s", v1.Schema)
	}
	latest, err := s.GetVersion(ctx, a, tmpl.ID, 0)
	if err != nil || latest.Version != 2 || latest.Source.HTML != "" {
		t.Fatalf("latest %+v %v", latest, err)
	}
	for _, tc := range []struct {
		project uuid.UUID
		id      uuid.UUID
		version int
	}{{b, tmpl.ID, 1}, {a, tmpl.ID, 3}, {a, uuid.New(), 1}, {a, tmpl.ID, -1}} {
		if _, err := s.GetVersion(ctx, tc.project, tc.id, tc.version); !errors.Is(err, templates.ErrNotFound) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
	empty, err := s.Create(ctx, a, "empty", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetVersion(ctx, a, empty.ID, 0); !errors.Is(err, templates.ErrNotFound) {
		t.Fatalf("latest of a template without versions: %v", err)
	}

	// The database refuses every change, whatever the caller.
	for _, stmt := range []string{
		"UPDATE template_versions SET subject = 'changed'",
		"UPDATE template_versions SET variables = '{}' WHERE version = 1",
		"DELETE FROM template_versions",
		"TRUNCATE template_versions CASCADE",
	} {
		_, err := pool.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "23001" {
			t.Errorf("%s: %v, want restrict_violation", stmt, err)
		}
	}
	again, err := s.GetVersion(ctx, a, tmpl.ID, 1)
	if err != nil || again.Source != v1src {
		t.Fatalf("after refused changes: %+v %v", again.Source, err)
	}
	// A stored version compiles and renders.
	c, err := again.Compile()
	if err != nil {
		t.Fatal(err)
	}
	r, err := c.Render([]byte(`{"name":"<Ada>"}`))
	if err != nil || r.HTML != "<p>\n  &lt;Ada&gt;\n</p>" {
		t.Fatalf("render %+v %v", r, err)
	}
}

func TestEmailTemplateReferenceStaysInProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	s := templates.NewStore(pool, 200, 500, 1<<20)
	tmpl, err := s.Create(ctx, a, "welcome", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Publish(ctx, a, tmpl.ID, src, []byte(schema)); err != nil {
		t.Fatal(err)
	}
	insert := func(project uuid.UUID, id any, version any) error {
		_, err := pool.Exec(ctx, `INSERT INTO emails (project_id, sender, recipient, subject, template_id, template_version)
			VALUES ($1, 'a@example.test', 'b@example.test', 'Hi', $2, $3)`, project, id, version)
		return err
	}
	if err := insert(a, tmpl.ID, 1); err != nil {
		t.Fatalf("own template: %v", err)
	}
	for name, err := range map[string]error{
		"other project":      insert(b, tmpl.ID, 1),
		"unknown version":    insert(a, tmpl.ID, 2),
		"id without version": insert(a, tmpl.ID, nil),
		"version without id": insert(a, nil, 1),
	} {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || (pgErr.Code != "23503" && pgErr.Code != "23514") {
			t.Errorf("%s: %v, want FK or CHECK violation", name, err)
		}
	}
}

func TestPublishStorageBudgetPerProject(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool, a, b := setup(t)
	size := int64(len(src.Subject) + len(src.Text) + len(src.HTML))
	s := templates.NewStore(pool, 200, 500, 2*size)
	ta, err := s.Create(ctx, a, "a", "")
	if err != nil {
		t.Fatal(err)
	}
	tb, err := s.Create(ctx, b, "b", "")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range 5 {
		wg.Go(func() { _, errs[i] = s.Publish(ctx, a, ta.ID, src, []byte(schema)) })
	}
	wg.Wait()
	ok := 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case !errors.Is(err, templates.ErrStorageLimit):
			t.Fatalf("unexpected: %v", err)
		}
	}
	if ok != 2 {
		t.Fatalf("%d concurrent publishes fit a budget of two versions", ok)
	}
	// Another project has its own budget.
	if _, err := s.Publish(ctx, b, tb.ID, src, []byte(schema)); err != nil {
		t.Fatalf("other project: %v", err)
	}
}
