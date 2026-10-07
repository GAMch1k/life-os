# Dashboard

The dashboard is a Next.js App Router application using TypeScript, Tailwind, and installed shadcn/ui components. It provides the unified navigation shell and Finance, Tasks, Inbox, and Calendar screens. Route-specific loading and failure states preserve the rest of the application when one backend is unavailable.

Browser API calls are relative to the current origin: `/api/finance`, `/api/tasks`, `/api/inbox`, and `/api/calendar`. This is essential: it keeps Docker service names and ports out of browser configuration and makes the gateway the single public boundary. Generated TypeScript declarations live under `src/lib/api/generated`; do not hand-copy contract types.

Run dashboard-only checks with `make dashboard-check`. For an integrated browser-facing check use `make life-smoke`, which creates service data through the gateway and verifies it renders in the dashboard.
