# Dashboard

The Dashboard is a Next.js App Router/TypeScript application using Tailwind and shadcn/ui. It provides the overview and routed Finance, Tasks, Inbox, and Calendar pages. It consumes generated TypeScript OpenAPI types under `src/lib/api/generated`, not hand-written transport copies.

Browser requests use `/api/finance`, `/api/tasks`, `/api/inbox`, `/api/calendar`, and `/api/voice` relative to the current origin. Server-side rendering uses Docker service DNS internally. Loading/error handling is page-scoped so an unavailable service does not require exposing direct service ports.

The overview Voice capture control uses the browser `MediaRecorder` API. Stopping a recording uploads it to the asynchronous Voice job API and immediately confirms acceptance; it never waits for Whisper. An upload error stays visible with a retry control, and the completed transcript appears through the normal Inbox widget. Run `make dashboard-check` for format/lint/test/type/build and `make life-smoke` for browser-facing behavior.
