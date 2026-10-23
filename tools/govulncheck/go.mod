// govulncheck has its own modfile so its golang.org/x/tools version never
// constrains the linters' modfiles.
// Run via: go tool -modfile=tools/govulncheck/go.mod govulncheck
module github.com/skraus2/email-platform/tools/govulncheck

go 1.26.0

toolchain go1.27.3

tool golang.org/x/vuln/cmd/govulncheck

require (
	golang.org/x/mod v0.42.0 // indirect
	golang.org/x/sync v0.24.0 // indirect
	golang.org/x/sys v0.49.0 // indirect
	golang.org/x/telemetry v0.0.0-20261010193011-f71dc92da45e // indirect
	golang.org/x/tools v0.52.0 // indirect
	golang.org/x/vuln v1.9.0 // indirect
)
