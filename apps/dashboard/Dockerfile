FROM node:22-alpine AS dependencies
WORKDIR /app
COPY apps/dashboard/package.json apps/dashboard/package-lock.json ./
RUN npm ci

FROM node:22-alpine AS build
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

FROM node:22-alpine AS runtime
WORKDIR /app
ENV NODE_ENV=production \
    NEXT_TELEMETRY_DISABLED=1 \
    PORT=3000
COPY --from=build /app/node_modules ./node_modules
COPY --from=build /app/.next ./.next
# next start loads next.config.ts when it starts; .next doesn't carry those
# settings for it. Without the file the server runs on Next.js's defaults
# and, for one, serves the Image Optimization API (/_next/image) that
# images.unoptimized turns off.
COPY --from=build /app/next.config.ts ./next.config.ts
# Static files served at the root: the app icons and the service worker.
COPY --from=build /app/public ./public
COPY --from=build /app/package.json ./package.json
EXPOSE 3000
CMD ["npm", "run", "start"]
