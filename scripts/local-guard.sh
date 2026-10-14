# Sourced by the process-level scripts. They start real binaries against a
# database and stop/start Docker containers, so they refuse to run against
# anything but the local Compose stack.
# More than one "@" or a host/hostaddr parameter could redirect the
# connection away from the host the pattern below checks.
if [[ "${DATABASE_URL:-}" == *@*@* || "${DATABASE_URL:-}" == *host=* || "${DATABASE_URL:-}" == *hostaddr=* ]]; then
  echo "refusing to run: DATABASE_URL must be a plain local URL" >&2
  exit 1
fi
case "${DATABASE_URL:-}" in
  postgres://*@localhost:*/* | postgres://*@127.0.0.1:*/* | postgres://*@\[::1\]:*/* | postgresql://*@localhost:*/* | postgresql://*@127.0.0.1:*/*) ;;
  *) echo "refusing to run: DATABASE_URL must point at the local Compose database (localhost)" >&2; exit 1 ;;
esac
if [[ -n "${DOCKER_HOST:-}" ]]; then
  echo "refusing to run: DOCKER_HOST is set; these scripts only drive the local Docker engine" >&2
  exit 1
fi
