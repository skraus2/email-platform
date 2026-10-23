# Caddy without the file capability on its binary: the kernel refuses to
# execute a binary with file capabilities under no-new-privileges. Binding
# 80/443 needs no capability because the shared network namespace allows
# unprivileged ports.
FROM caddy:2.11.6-alpine@sha256:d44355d3c2149dc580ce2cac735955d1c08d3d00882c30489c241aa51a5c10d9
RUN setcap -r /usr/bin/caddy
