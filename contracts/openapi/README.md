# Life OS OpenAPI conventions

OpenAPI documents define the public HTTP boundary of a service. They are not
domain models: generated DTOs belong in transport adapters, which explicitly
translate to and from the service's own domain types.

## URL and versioning

- Business endpoints are rooted at `/v1`, for example
  `/v1/accounts/{accountId}`.
- A breaking HTTP-contract change receives a new major path version. Additive,
  backward-compatible changes remain in the current version.
- `/health` and `/ready` are operational endpoints supplied by the shared
  runtime and intentionally sit outside `/v1`.
- Use plural, lowercase kebab-case resource paths. Path parameters use a
  singular lowerCamelCase identifier ending in `Id`.

## JSON and data values

- Request and response property names are lowerCamelCase.
- Resource identifiers are UUID strings with `format: uuid`; IDs are opaque to
  clients and services must not encode domain meaning in them.
- Timestamps are RFC 3339 UTC instants with `format: date-time`. Calendar-only
  values use ISO 8601 dates with `format: date`; do not serialize a date as a
  midnight timestamp.
- Durations use ISO 8601 duration strings when an API needs one.
- Optional fields are omitted when absent. Use `nullable: true` only when a
  client must distinguish an explicit JSON `null` from omission.

## Operations, requests, and lists

- Operations use `operationId` in lowerCamelCase, beginning with a verb such
  as `listTasks`, `createTask`, or `completeTask`.
- Create request schemas end in `CreateRequest`; update schemas end in
  `UpdateRequest`; successful resource responses use the resource noun.
- List endpoints return an object with an `items` array and a nullable
  `nextCursor` string. They accept an opaque `cursor` query parameter and an
  optional integer `limit` (1–100, default 50). Cursor ordering is stable and
  documented by the owning contract.
- Filtering and sorting are explicit query parameters. Do not expose database
  expressions or generic field-selection syntax.

## Errors

Every non-success response that carries a body uses the reusable
`ErrorResponse` schema from [`common.yaml`](common.yaml). It contains a stable
machine-readable `code`, a safe human-readable `message`, and a request
`requestId` for support correlation. Validation failures may include `details`
with per-field `field` and `message` values. Never expose stack traces,
database errors, secrets, or internal type names.

Use conventional status codes: `400` malformed input, `404` missing resource,
`409` state conflict/duplicate, `422` semantically invalid request, and `500`
unexpected server failure. Authentication and authorization statuses are added
only when that boundary is introduced.

## Contract ownership

Each service owns its OpenAPI file under `services/<service>/api/openapi.yaml`.
Shared documents contain only transport-level schemas and conventions. A
service may reuse `ErrorResponse`, but it must never import another service's
domain package, database schema, or internal Go structs.

`example.yaml` is the minimal validating example for these rules. Contract
generation and lint/drift automation are added by later tickets.
