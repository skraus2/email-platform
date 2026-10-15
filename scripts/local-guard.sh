# Sourced by the process-level scripts. They start real binaries against a
# database and stop/start Docker containers, so they refuse to run against
# anything but the local Compose stack: a plain URL with exactly one host,
# which must be loopback, and no parameters other than sslmode (multi-host
# lists, host=/hostaddr= parameters or extra "@" could redirect pgx).
local_db_re='^postgres(ql)?://[^@/?#,]+@(localhost|127\.0\.0\.1|\[::1\]):[0-9]+/[A-Za-z0-9_]+(\?sslmode=[a-z-]+)?$'
if [[ ! "${DATABASE_URL:-}" =~ $local_db_re ]]; then
  echo "refusing to run: DATABASE_URL must be a plain URL of the local Compose database (localhost)" >&2
  exit 1
fi
if [[ -n "${DOCKER_HOST:-}" ]]; then
  echo "refusing to run: DOCKER_HOST is set; these scripts only drive the local Docker engine" >&2
  exit 1
fi
# The processes started here must never reach AWS, whatever the caller's
# shell holds: with the local sink forced, any stray SES_* or SQS_ENDPOINT
# setting is a startup error instead of an SES-mode worker.
export MAIL_PROVIDER=mailpit
