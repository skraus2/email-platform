# Sourced by the process-level scripts. They start real binaries against a
# database and stop/start Docker containers, so they refuse to run against
# anything but the local Compose stack.
case "${DATABASE_URL:-}" in
  postgres://*@localhost:*/* | postgres://*@127.0.0.1:*/* | postgres://*@\[::1\]:*/* | postgresql://*@localhost:*/* | postgresql://*@127.0.0.1:*/*) ;;
  *) echo "refusing to run: DATABASE_URL must point at the local Compose database (localhost)" >&2; exit 1 ;;
esac
if [[ -n "${DOCKER_HOST:-}" ]]; then
  echo "refusing to run: DOCKER_HOST is set; these scripts only drive the local Docker engine" >&2
  exit 1
fi
