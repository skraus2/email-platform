package jobs

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
)

// River ships its schema as versioned SQL. We embed it in the goose history
// instead of running rivermigrate, so there is one migration history.
//
// Upgrading River: bump the version in go.mod, run this test; it fails and
// lists versions that are missing. Add them as a NEW goose migration
// (never edit an applied one) using the same "-- river migration NNN" markers.
// -update only writes the initial file below when it does not exist yet.
var update = flag.Bool("update", false, "write the initial River goose migration if missing")

const initialRiverMigration = "00003_river_queue.sql"

var markerRE = regexp.MustCompile(`(?m)^-- river migration (\d{3}): .* \(up\)\n`)

func riverVersions(t *testing.T) []rivermigrate.Migration {
	t.Helper()
	m, err := rivermigrate.New[pgx.Tx](riverpgxv5.New(nil), nil)
	if err != nil {
		t.Fatal(err)
	}
	return m.AllVersions()
}

// schemaTemplate is River's schema placeholder. rivermigrate replaces it before
// executing; it also appears inside string literals (to_regclass('...')), so
// it must be removed, not left as a comment. Empty = default search_path.
const schemaTemplate = "/* TEMPLATE: schema */"

func riverSQL(s string) string { return strings.TrimSpace(strings.ReplaceAll(s, schemaTemplate, "")) }

func renderInitial(versions []rivermigrate.Migration) string {
	var b strings.Builder
	b.WriteString("-- River job queue schema, generated from River's own migrations\n")
	b.WriteString("-- (internal/jobs/migration_test.go). Never edit; River upgrades add a new\n")
	b.WriteString("-- migration with the missing versions.\n\n")
	b.WriteString("-- +goose Up\n-- +goose StatementBegin\n")
	for _, v := range versions {
		fmt.Fprintf(&b, "-- river migration %03d: %s (up)\n%s\n", v.Version, v.Name, riverSQL(v.SQLUp))
	}
	b.WriteString("INSERT INTO river_migration (line, version) VALUES\n")
	for i, v := range versions {
		sep := ","
		if i == len(versions)-1 {
			sep = ";"
		}
		fmt.Fprintf(&b, "    ('main', %d)%s\n", v.Version, sep)
	}
	b.WriteString("-- +goose StatementEnd\n\n-- +goose Down\n-- +goose StatementBegin\n")
	for i := len(versions) - 1; i >= 0; i-- {
		v := versions[i]
		fmt.Fprintf(&b, "-- river migration %03d: %s (down)\n%s\n", v.Version, v.Name, riverSQL(v.SQLDown))
	}
	b.WriteString("-- +goose StatementEnd\n")
	return b.String()
}

func TestRiverSchemaMigrationsMatchRiverVersion(t *testing.T) {
	versions := riverVersions(t)
	dir := filepath.Join("..", "..", "db", "migrations")
	initial := filepath.Join(dir, initialRiverMigration)
	if *update {
		if _, err := os.Stat(initial); err == nil {
			t.Fatalf("%s exists; never rewrite an applied migration", initial)
		}
		if err := os.WriteFile(initial, []byte(renderInitial(versions)), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Collect every River version embedded in any migration file.
	files, err := filepath.Glob(filepath.Join(dir, "*.sql"))
	if err != nil {
		t.Fatal(err)
	}
	embedded := map[int]string{}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		locs := markerRE.FindAllStringSubmatchIndex(text, -1)
		for i, loc := range locs {
			n, _ := strconv.Atoi(text[loc[2]:loc[3]])
			if _, dup := embedded[n]; dup {
				t.Fatalf("river migration %03d embedded twice", n)
			}
			end := len(text)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			embedded[n] = text[loc[1]:end]
		}
	}

	for _, v := range versions {
		body, ok := embedded[v.Version]
		if !ok {
			t.Errorf("river migration %03d (%s) is not in db/migrations — add it as a new migration", v.Version, v.Name)
			continue
		}
		if !strings.HasPrefix(body, riverSQL(v.SQLUp)) {
			t.Errorf("river migration %03d differs from River's SQL", v.Version)
		}
	}
	if len(embedded) > len(versions) {
		t.Errorf("db/migrations embeds %d River versions, River knows %d", len(embedded), len(versions))
	}
}
