package store

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// unscopedQueries may touch project tables without a project_id predicate.
// Every entry names why that is safe; anything else must filter by project.
var unscopedQueries = map[string]string{
	// Operator console: operators see every project by design.
	"AdminGetEmail":         "operator console (all projects)",
	"AdminGetTemplate":      "operator console (all projects)",
	"AdminGetBroadcast":     "operator console (all projects)",
	"AdminGetAutomation":    "operator console (all projects)",
	"AdminGetAutomationRun": "operator console (all projects)",
	// Operator lists: the project filter is optional (NULL = all projects).
	"AdminSearchEmails":         "operator console, optional project filter",
	"AdminListTemplates":        "operator console, optional project filter",
	"AdminListSenders":          "operator console, optional project filter",
	"AdminListWebhookEndpoints": "operator console, optional project filter",
	"AdminListBroadcasts":       "operator console, optional project filter",
	"AdminListAutomations":      "operator console, optional project filter",
	// Children of a parent the caller already loaded by id and project.
	"CountAudienceMembers":   "audience checked by caller; composite FK pins members to its project",
	"CountAudienceMembersIn": "audience checked by caller; composite FK pins members to its project",
	"CountBroadcastEmails":   "broadcast checked by caller; composite FK pins emails to its project",
	// Maintenance and safety nets over all projects; they return or act on
	// rows carrying their own project_id.
	"OverdueRuns":                      "safety net; re-enqueues with each run's own project_id",
	"ExpireOpenEmails":                 "safety net; returns each email's project_id",
	"ExpireStaleDeliveries":            "safety net over all projects",
	"DeleteOldDeliveries":              "retention over all projects",
	"DeleteOldEvents":                  "retention over all projects",
	"DeleteOldReentryRuns":             "retention over all projects",
	"DeleteContentsBefore":             "retention safety net over all projects",
	"LockOldEmails":                    "opt-in email retention over all projects",
	"DeleteLockedEmails":               "opt-in email retention; ids come from LockOldEmails",
	"DeleteOldContactEvents":           "retention over all projects",
	"DeleteIdempotencyKeysOlderThan":   "retention over all projects",
	"DeleteRateLimitWindowsBefore":     "maintenance over all projects",
	"DeleteContactEventWindowsBefore":  "maintenance over all projects",
	"DeleteTemplateWriteWindowsBefore": "maintenance over all projects",
	"LastUsageRollup":                  "time of the last usage rollup (no project data)",
	"RollupUsage":                      "usage aggregate over all projects; every row keeps its project_id",
	"LockEndpointsNotUnderKey":         "secret key rotation over all projects (admin CLI)",
	// Provider-wide accounting: one daily cap per provider account.
	"CountProviderHandoffs": "provider-wide daily cap (shared by all projects)",
	// Monitoring: platform-wide totals exported as metrics, no project data.
	"CountOpenEmails":       "platform-wide open email counts for metrics",
	"WebhookBacklog":        "platform-wide webhook backlog for metrics",
	"HandoffLeavesWindowIn": "provider-wide daily cap (shared by all projects)",
	// The project is not known yet: these resolve it.
	"GetActiveKeyByPrefix": "authentication resolves the project from the key",
	"RevokeAPIKey":         "admin CLI revokes by globally unique key prefix",
	"ProviderEventOwner":   "duplicate-owner check on a provider-assigned global event id",
}

// param is a sqlc query parameter.
const param = `(\$\d+|sqlc\.n?arg\(|@\w)`

var (
	queryName = regexp.MustCompile(`(?m)^-- name: (\w+)`)
	comment   = regexp.MustCompile(`--[^\n]*`)
	// FROM, JOIN, UPDATE, DELETE FROM and INSERT INTO a table.
	tableRef = regexp.MustCompile(`(?i)\b(from|join|update|into)\s+([a-z_]+)`)
	// project_id compared with a query parameter: "project_id = $1",
	// "project_id = sqlc.arg(…)", "project_id = ANY ($1)", "$1 = x.project_id"
	// or "(…, project_id) = ($1, …)". Comparing two tables' project_id only
	// links rows and scopes nothing.
	projectPredicate = regexp.MustCompile(`(?i)\b(\w+\.)?project_id\s*=\s*(any\s*\(\s*)?` + param +
		`|` + param + `\s*=\s*(\w+\.)?project_id\b` +
		`|\([^()]*\bproject_id\b[^()]*\)\s*(=|in)\s*\(\s*` + param)
	insertColumns = regexp.MustCompile(`(?is)^\s*insert\s+into\s+[a-z_]+(\s+as\s+\w+)?\s*\(([^)]*)\)`)
	// "sqlc.narg(project_id) IS NULL OR project_id = …" scopes nothing when
	// the argument is NULL.
	optionalProject = regexp.MustCompile(`(?i)(sqlc\.narg\(\s*'?|@)\w*project_id'?\s*\)?(::uuid)?\s+is\s+null\b` +
		`|\$\d+(::uuid)?\s+is\s+null\s+or\s+(\w+\.)?project_id\b`)
	// Joining the project registry names a project; it scopes nothing.
	projectJoin = regexp.MustCompile(`(?i)\bjoin\s+projects\s+\w+\s+on\s+\w+\.id\s*=\s*\w+\.project_id\b`)
)

// TestQueriesScopeByProject is a tripwire over db/queries: a query that reads
// or changes project tables must compare project_id (or insert it), unless it
// is listed above with a reason. The integration tests prove the behaviour;
// this test makes a new unscoped query a deliberate, reviewed decision.
func TestQueriesScopeByProject(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "db", "queries", "*.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no query files: %v", err)
	}
	seen := map[string]bool{}
	scoped := 0
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		names := queryName.FindAllStringSubmatchIndex(string(raw), -1)
		for i, m := range names {
			name := string(raw[m[2]:m[3]])
			end := len(raw)
			if i+1 < len(names) {
				end = names[i+1][0]
			}
			start := m[1] + strings.IndexByte(string(raw[m[1]:end]), '\n') + 1
			body := comment.ReplaceAllString(string(raw[start:end]), "")
			seen[name] = true
			if !touchesProjectTables(body) {
				continue
			}
			ok := scopedByProject(body)
			_, listed := unscopedQueries[name]
			switch {
			case ok && listed:
				t.Errorf("%s (%s) filters by project but is listed as unscoped", name, filepath.Base(f))
			case !ok && !listed:
				t.Errorf("%s (%s) touches project tables without a project_id predicate; scope it or list it with a reason", name, filepath.Base(f))
			case ok:
				scoped++
			}
		}
	}
	for name := range unscopedQueries {
		if !seen[name] {
			t.Errorf("stale unscoped entry %s", name)
		}
	}
	if scoped < 100 {
		t.Fatalf("only %d scoped queries found; parsing broke?", scoped)
	}
}

func touchesProjectTables(body string) bool {
	for _, m := range tableRef.FindAllStringSubmatch(body, -1) {
		if slices.Contains(tenantTables, strings.ToLower(m[2])) {
			return true
		}
	}
	return false
}

// scopedByProject: a project_id comparison anywhere, or a plain INSERT …
// VALUES that writes project_id and reads no project table.
func scopedByProject(body string) bool {
	if optionalProject.MatchString(body) {
		return false
	}
	if projectPredicate.MatchString(projectJoin.ReplaceAllString(body, "")) {
		return true
	}
	cols := insertColumns.FindStringSubmatch(body)
	if cols == nil || !regexp.MustCompile(`\bproject_id\b`).MatchString(cols[2]) {
		return false
	}
	for _, m := range tableRef.FindAllStringSubmatch(body, -1) {
		if strings.ToLower(m[1]) != "into" && slices.Contains(tenantTables, strings.ToLower(m[2])) {
			return false
		}
	}
	return true
}
