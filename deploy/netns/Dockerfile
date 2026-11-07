# Owner of the stack's shared network namespace. Before anything else joins
# the namespace it limits the EC2 instance metadata service (and with it the
# instance role's credentials) to the application image's user; the proxy,
# dashboard, Prometheus and Alertmanager cannot reach it.
FROM alpine:3.24@sha256:93b41ef40b1677fce8fcc64ff4af09daddfacf5040afde618bc46c1e2d1cc7d4
RUN apk add --no-cache iptables~1.8.13
# This image release predates the fixed zlib package.
RUN apk upgrade --no-cache zlib
COPY guard.sh /usr/local/bin/guard
ENTRYPOINT ["/usr/local/bin/guard"]
