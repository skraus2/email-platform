# syntax=docker/dockerfile:1
# Builds the operator dashboard (Next.js standalone server) into a small,
# non-root image. Build from the repository root:
#   docker build -f apps/dashboard/Dockerfile -t email-platform-dashboard .
# It listens on 127.0.0.1:3000 by default (HOSTNAME/PORT) and reads the
# operator API from ADMIN_API_URL at run time.

FROM node:24.21.0-bookworm-slim@sha256:d6aa754f16b3197301076f047b5def2f02ea1dbbc2ca920407d46d7ec7f87b20 AS build
WORKDIR /src
RUN corepack enable
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml ./
COPY apps/dashboard/package.json apps/dashboard/
RUN pnpm install --frozen-lockfile --filter dashboard
COPY apps/dashboard apps/dashboard
ENV NEXT_TELEMETRY_DISABLED=1 NEXT_OUTPUT=standalone
RUN pnpm --filter dashboard build

FROM node:24.21.0-alpine3.24@sha256:ebfe2f90462722a7a4de65e91990e97fe0d401c70e0e762c5b53302f905ec1c1
WORKDIR /app
ENV NODE_ENV=production NEXT_TELEMETRY_DISABLED=1 HOSTNAME=127.0.0.1 PORT=3000
# Owned by root and read-only for the runtime user.
COPY --from=build /src/apps/dashboard/.next/standalone ./
COPY --from=build /src/apps/dashboard/.next/static ./apps/dashboard/.next/static
USER node
EXPOSE 3000
CMD ["node", "apps/dashboard/server.js"]
