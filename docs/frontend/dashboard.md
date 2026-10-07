# Dashboard

The Dashboard is a Next.js App Router/TypeScript application using Tailwind and shadcn/ui. It provides the overview and routed Finance, Tasks, Inbox, and Calendar pages. It consumes generated TypeScript OpenAPI types under `src/lib/api/generated`, not hand-written transport copies.

Browser requests use `/api/finance`, `/api/tasks`, `/api/inbox`, and `/api/calendar` relative to the current origin. Server-side rendering uses Docker service DNS internally. Loading/error handling is page-scoped so an unavailable service does not require exposing direct service ports. Run `make dashboard-check` for format/lint/test/type/build and `make life-smoke` for browser-facing behavior.
