FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS dependencies
WORKDIR /app
COPY apps/dashboard/package.json apps/dashboard/package-lock.json ./
RUN npm ci

# The runtime's node_modules: the production dependencies alone, without the
# build and test tools (Next.js builds in the build stage).
FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS production-dependencies
WORKDIR /app
COPY apps/dashboard/package.json apps/dashboard/package-lock.json ./
RUN npm ci --omit=dev

FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS build
WORKDIR /app
ARG NEXT_PUBLIC_FINANCE_SERVICE_URL=http://localhost:8081
ARG NEXT_PUBLIC_TASKS_SERVICE_URL=http://localhost:8082
ARG NEXT_PUBLIC_INBOX_SERVICE_URL=http://localhost:8083
ARG NEXT_PUBLIC_CALENDAR_SERVICE_URL=http://localhost:8084
ENV NEXT_TELEMETRY_DISABLED=1 \
    NEXT_PUBLIC_FINANCE_SERVICE_URL=$NEXT_PUBLIC_FINANCE_SERVICE_URL \
    NEXT_PUBLIC_TASKS_SERVICE_URL=$NEXT_PUBLIC_TASKS_SERVICE_URL \
    NEXT_PUBLIC_INBOX_SERVICE_URL=$NEXT_PUBLIC_INBOX_SERVICE_URL \
    NEXT_PUBLIC_CALENDAR_SERVICE_URL=$NEXT_PUBLIC_CALENDAR_SERVICE_URL
COPY --from=dependencies /app/node_modules ./node_modules
COPY apps/dashboard ./
RUN npm run build

FROM node:22-alpine@sha256:0e1754758c2df4cfa3e68d59e2e1b7374bb7fd9ea6328eaaacf15cc934af8202 AS runtime
# The image's source and license; on ghcr.io this links its package to the
# repository (docs/operations/releases.md).
LABEL org.opencontainers.image.source="https://github.com/GAMch1k/life-os" \
      org.opencontainers.image.licenses="AGPL-3.0-only"
WORKDIR /app
# The Dashboard answers the gateway's auth check for every API call. Left to
# itself, V8 sizes its heap by the host's memory and collects lazily: the
# process grew to about 440 MiB under a load of checks on a 7.7 GiB host,
# against about 210 MiB with this cap, which is also faster (P21-03).
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    NODE_OPTIONS=--max-old-space-size=256 \
    PORT=3000
# Nothing here runs npm, npx, corepack, or yarn: Next.js starts with node
# below. Their bundled packages were all of the image's HIGH findings
# (SEC-AUDIT).
# The base image is pinned by digest and moves only when Dependabot bumps it;
# Alpine's security fixes land in between, and the release's Trivy gate fails
# on a HIGH or CRITICAL vulnerability that has a fix: the build upgrades the
# packages (SEC-IMG).
RUN apk upgrade --no-cache \
    && rm -rf /usr/local/lib/node_modules/npm /usr/local/lib/node_modules/corepack \
    /usr/local/bin/npm /usr/local/bin/npx /usr/local/bin/corepack \
    /opt/yarn-* /usr/local/bin/yarn /usr/local/bin/yarnpkg
COPY --from=production-dependencies /app/node_modules ./node_modules
# .next belongs to node, which the server runs as: Next.js writes its cache
# there (.next/cache). Everything else stays root's, read-only to the server.
COPY --from=build --chown=node:node /app/.next ./.next
# next start loads next.config.ts when it starts; .next doesn't carry those
# settings for it. Without the file the server runs on Next.js's defaults
# and, for one, serves the Image Optimization API (/_next/image) that
# images.unoptimized turns off.
COPY --from=build /app/next.config.ts ./next.config.ts
# Static files served at the root: the app icons and the service worker.
COPY --from=build /app/public ./public
COPY --from=build /app/package.json ./package.json
# The account commands ./setup.sh runs in this container (setup-code,
# recover-owner, legacy-access, sign-out-all; docs/install/accounts.md).
COPY --from=build /app/scripts/lifeos-auth.mjs ./scripts/lifeos-auth.mjs
USER node
EXPOSE 3000
# package.json's start script (next start), run by node itself rather than
# through npm. next start listens on PORT, on every address, as before.
CMD ["node", "node_modules/next/dist/bin/next", "start"]
