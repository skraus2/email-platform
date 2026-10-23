package store

// Every table of the schema is either global or belongs to one project. The
// schema test (integration) proves this split matches the migrated
// database, so a new table must be classified here before tests pass.

// globalTables hold no project data, or are the project registry itself.
var globalTables = map[string]string{
	"projects":           "the tenant registry",
	"admin_users":        "operator accounts, not project members",
	"admin_sessions":     "operator sessions",
	"goose_db_version":   "migration bookkeeping",
	"river_job":          "job queue; project scoping lives in the job args",
	"river_leader":       "job queue bookkeeping",
	"river_migration":    "job queue bookkeeping",
	"river_notification": "job queue bookkeeping",
	"river_queue":        "job queue bookkeeping",
}

// tenantTables carry project_id on every row.
var tenantTables = []string{
	"api_keys",
	"audience_members",
	"audiences",
	"automation_runs",
	"automations",
	"broadcasts",
	"contact_event_windows",
	"contact_events",
	"contacts",
	"email_contents",
	"email_events",
	"emails",
	"idempotency_keys",
	"rate_limit_windows",
	"sender_identities",
	"suppressions",
	"template_versions",
	"template_write_windows",
	"templates",
	"webhook_deliveries",
	"webhook_endpoints",
}
