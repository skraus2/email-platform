# Email Platform

A self-hosted transactional and marketing email platform for a handful of
internal applications: an HTTP API that accepts emails, a worker that sends
them through Amazon SES and tracks what happens to them, and an operator
dashboard.

Go (API and worker as one modular monolith), PostgreSQL, Next.js.

## What it does

- **Sending:** `POST /v1/emails` with idempotency keys, per-project rate
  limits, suppression lists, verified senders, scheduled sends and
  cancellation; persistent jobs with retries and capped exponential backoff.
- **Delivery tracking:** SES events (send, delivery, bounce, complaint,
  delay) arrive through SNS → SQS; SNS signatures are verified, events are
  stored immutably and projected to `delivered` / `bounced` / `complained`;
  bounces and complaints suppress the address.
- **Webhooks:** every status change is posted to the project's endpoints,
  signed with HMAC-SHA256 (`Webhook-Signature: v1=…` over
  `<timestamp>.<body>`), retried with backoff; secrets encrypted at rest.
- **Templates:** immutable template versions with a typed variable schema
  and a restricted construct allowlist; HTML is escaped.
- **Contacts, broadcasts, automations:** contacts with consent status,
  audiences, unsubscribe links (List-Unsubscribe), broadcasts to an
  audience, event-triggered automations (wait / condition / send).
- **Operations:** per-project daily usage (`GET /v1/usage`), Prometheus
  metrics with tested alert rules, an operator dashboard (read-only, local
  accounts), a production Compose stack for one VM with Amazon RDS, and
  CloudFormation templates for the AWS resources.

API contract: [`api/openapi.yaml`](api/openapi.yaml) (operator API:
[`api/admin-openapi.yaml`](api/admin-openapi.yaml)).

## Status

Built for internal use; one tenant per project, every project's data
isolated by `project_id`. The full stack has been deployed to AWS once
(EC2 + RDS + SES, eu-north-1), tested end to end with real delivery, and
torn down again. There is no hosted offering, no public sign-up and no
billing.

## Quick start (local, no real email)

Local development sends to [Mailpit](https://mailpit.axllent.org/) only;
nothing leaves your machine and no AWS service is contacted.

Prerequisites: Go ≥ 1.26 (the pinned toolchain downloads itself), pnpm ≥ 10
(downloads the pinned Node), Docker with Compose v2, make, curl.

```bash
pnpm install
make up            # PostgreSQL on 127.0.0.1:54320, Mailpit UI on :8025
make migrate
echo "WEBHOOK_KEKS=dev:$(openssl rand -base64 32)" >> .env   # webhook secret key, never commit .env
make run-api       # API on http://127.0.0.1:8080
make run-worker    # in a second terminal
```

Send an email:

```bash
make admin ARGS="project create -name my-app"        # prints <project-id>
make admin ARGS="key create -project <project-id>"   # prints the API key once
curl -s -X POST http://127.0.0.1:8080/v1/emails \
  -H "Authorization: Bearer <key>" -H "Content-Type: application/json" \
  -H "Idempotency-Key: welcome-42" \
  -d '{"from":"noreply@example.test","to":"someone@example.test","subject":"Hi","text":"Hello"}'
curl -s http://127.0.0.1:8080/v1/emails/<email-id> -H "Authorization: Bearer <key>"
```

The message shows up in Mailpit (<http://127.0.0.1:8025>) with status
`captured`; Mailpit never produces `delivered`.

Dashboard:

```bash
read -rs PW && printf '%s\n' "$PW" | make admin ARGS="user create -username alice"; unset PW
make run-dashboard # http://127.0.0.1:3000
```

## Tests and checks

```bash
make check             # formatting, linters, unit tests, build (no Docker)
make test-integration  # integration tests, a throwaway database per test (needs make up)
make smoke             # real binaries end to end (needs make up migrate)
make test-e2e          # API + worker + dashboard with Playwright
make deploy-check      # the production Compose stack locally, with TLS everywhere
make restore-test      # backup and restore drill
make vulncheck         # known vulnerabilities in Go and npm dependencies
make imagecheck        # known vulnerabilities in the production images
make ci                # everything CI runs
```

CI (GitHub Actions) runs the same targets; it uses no secrets and no cloud
access. Dependabot proposes weekly updates.

## Deployment

- `deploy/compose.yaml`: api, worker, dashboard, Caddy (TLS via Let's
  Encrypt, proxies only `/v1/*` and the health probes), Prometheus and
  Alertmanager in one shared network namespace; only the application user
  may reach the instance metadata service.
- `deploy/bin/render-env`: runtime secrets from SSM Parameter Store with the
  instance role, written to tmpfs; `deploy/bin/preflight` refuses to start
  without them.
- `deploy/aws/`: CloudFormation stacks (SES event path with an encrypted
  SNS topic and SQS queue, SES identity with Easy DKIM, VPC + EC2 + RDS),
  `up` / `teardown` scripts and `leftovers`, which lists anything billable
  that remains.

The database needs `sslmode=verify-full`; there is no SSH access (Session
Manager only) and the dashboard is never published (SSM port forward).

## Known limitations

- Exactly-once delivery is impossible: an interrupted send may be retried
  and duplicated; such retries are logged.
- Sender verification in the platform is an operator assertion; SES
  enforces the real one.
- Rate limits are per project and per worker process; projects share the
  provider budget without fairness.
- Dashboard accounts are local (password only, no MFA or SSO), and every
  operator sees every project. Keep the dashboard and the API on a private
  network.

## Security

Please report vulnerabilities privately, see [SECURITY.md](SECURITY.md).

## License

[Apache License 2.0](LICENSE).
