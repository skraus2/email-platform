#!/bin/sh
# Runs as the first process of the shared network namespace: rejects traffic
# to the instance metadata service from every user except APP_UID, reports
# ready (healthcheck) and then idles until stopped.
set -eu

app_uid="${APP_UID:-65532}"
case "$app_uid" in *[!0-9]* | "") echo "guard: APP_UID must be a number" >&2; exit 1 ;; esac

block() { # <iptables binary> <metadata address>
  "$1" -w -C OUTPUT -d "$2" -m owner ! --uid-owner "$app_uid" -j REJECT 2>/dev/null ||
    "$1" -w -I OUTPUT 1 -d "$2" -m owner ! --uid-owner "$app_uid" -j REJECT
}

block iptables 169.254.169.254
if [ "$(cat /proc/sys/net/ipv6/conf/all/disable_ipv6 2>/dev/null || echo 1)" = 0 ]; then
  block ip6tables fd00:ec2::254
fi
touch /tmp/guard-ready
echo "guard: instance metadata reachable only for uid $app_uid"

trap 'exit 0' TERM INT
while :; do
  sleep 86400 &
  wait $! || true
done
