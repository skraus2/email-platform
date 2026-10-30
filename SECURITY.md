# Security policy

## Reporting a vulnerability

Please do not open a public issue for security problems. Report them
privately through GitHub: **Security → Report a vulnerability** in this
repository (private vulnerability reporting). Include what you found, how
to reproduce it and the commit you tested.

You can expect an acknowledgement within a week. Fixes land on `main`;
there are no maintained release branches.

## Supported versions

Only the latest commit on `main` is supported.

## Scope

In scope: the code in this repository — API, worker, dashboard, admin CLI,
the production Compose stack and the deployment scripts.

Out of scope: findings that need an already compromised operator account or
host, denial of service by volume, and vulnerabilities in third-party
dependencies that are already tracked upstream (please report those to the
upstream project).
