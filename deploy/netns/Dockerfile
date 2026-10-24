# Owner of the stack's shared network namespace. Before anything else joins
# the namespace it limits the EC2 instance metadata service (and with it the
# instance role's credentials) to the application image's user; the proxy,
# dashboard, Prometheus and Alertmanager cannot reach it.
FROM alpine:3.24@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache iptables~1.8.13
COPY guard.sh /usr/local/bin/guard
ENTRYPOINT ["/usr/local/bin/guard"]
