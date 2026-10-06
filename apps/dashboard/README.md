# Life OS dashboard

The dashboard is the browser interface for Life OS. It uses the Next.js App
Router, TypeScript, Tailwind CSS, and shadcn/ui. This foundation intentionally
contains only the application shell; domain screens arrive with their own
tickets.

## Run locally

The dashboard requires Node.js 22 or newer.

```sh
npm install
cp .env.example .env.local
npm run dev
```

Open <http://localhost:3000>.

For the full containerized local stack from the repository root, use
`make life-up`. It starts PostgreSQL, the core services, and this dashboard.

## Service endpoints

The browser-facing service URLs are configured at build time. The checked-in
example uses provisional local ports which can be overridden in `.env.local`:

| Variable                           | Default                 |
| ---------------------------------- | ----------------------- |
| `NEXT_PUBLIC_FINANCE_SERVICE_URL`  | `http://localhost:8081` |
| `NEXT_PUBLIC_TASKS_SERVICE_URL`    | `http://localhost:8082` |
| `NEXT_PUBLIC_INBOX_SERVICE_URL`    | `http://localhost:8083` |
| `NEXT_PUBLIC_CALENDAR_SERVICE_URL` | `http://localhost:8084` |

The typed and normalized configuration is exported from
`src/config/services.ts`. Because the variables use Next.js's `NEXT_PUBLIC_`
prefix, do not put credentials or other secrets in them.

## Quality checks

```sh
npm run format
npm run lint
npm run test
npm run typecheck
npm run build
npm run check
```

`npm run check` is the non-mutating aggregate command intended for the
repository-level check workflow.
