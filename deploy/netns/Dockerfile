# Owner of the stack's shared network namespace. Before anything else joins
# the namespace it limits the EC2 instance metadata service (and with it the
# instance role's credentials) to the application image's user; the proxy,
# dashboard, Prometheus and Alertmanager cannot reach it.
FROM alpine:3.22@sha256:2e0b81d60597f449baa56dc25d355367869d2dca4cd57e866f1e2b52af3eb55f
RUN apk add --no-cache iptables ip6tables
COPY guard.sh /usr/local/bin/guard
ENTRYPOINT ["/usr/local/bin/guard"]
