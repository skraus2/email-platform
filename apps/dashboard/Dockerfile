# syntax=docker/dockerfile:1
# Builds the operator dashboard (Next.js standalone server) into a small,
# non-root image. Build from the repository root:
#   docker build -f apps/dashboard/Dockerfile -t email-platform-dashboard .
# It listens on 127.0.0.1:3000 by default (HOSTNAME/PORT) and reads the
# operator API from ADMIN_API_URL at run time.

FROM node:26.13.0-trixie-slim@sha256:e4db4c914bf8ad241bd24804fc25d272d89bad97e68cdc8a1414862790a02c39 AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/dashboard/package.json apps/dashboard/
RUN pnpm install --frozen-lockfile --filter dashboard
COPY apps/dashboard apps/dashboard
ENV NEXT_TELEMETRY_DISABLED=1 NEXT_OUTPUT=standalone
RUN pnpm --filter dashboard build

FROM node:26.13.0-alpine3.24@sha256:c6ad9e9c1d80f6eb607c26b4b0a132ff7a2344fad1c78ea3baa2a7d4c8de6d94
# The server needs only the node binary: package managers (npm, corepack,
# yarn) and their bundled dependencies are removed from the runtime.
RUN rm -rf /usr/local/lib/node_modules /opt/yarn-* \
    /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack /usr/local/bin/yarn /usr/local/bin/yarnpkg
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=127.0.0.1 PORT=3000
# Owned by root and read-only for the runtime user.
COPY --from=build /src/apps/dashboard/.next/standalone ./
COPY --from=build /src/apps/dashboard/.next/static ./apps/dashboard/.next/static
USER node
EXPOSE 3000
CMD ["node", "apps/dashboard/server.js"]
