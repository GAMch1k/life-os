# Architecture

Each domain service owns its database, migrations, SQL queries, repository, domain logic, and OpenAPI contract. A service may call another service's API when necessary, but it must never read another service database. `pkg` provides infrastructure such as typed environment configuration, Fiber startup/shutdown, errors, request IDs, and logging; it contains no domain models.

```text
Browser → Docker Nginx → Dashboard or /api/<domain> → private service → owned PostgreSQL database
```

The Dashboard is a Next.js App Router application. Browser requests use gateway-relative paths. Server-side dashboard data fetches use Docker service DNS names, so no backend port needs publishing.

OpenAPI YAML is the transport source of truth. `oapi-codegen` produces Go bindings and `openapi-typescript` produces dashboard types. SQL migrations and sqlc queries are service-local. PostgreSQL is a shared server, not a shared database: Finance, Tasks, Inbox, and Calendar each get a database and login.
