# Food service

Food keeps the owner's groceries: one shopping list grouped by aisle, where an
item may be tagged with the store to buy it at; the stores; the aisle rules
that place new items; and the pantry, what is at home and when it expires. It
keeps them in `food_db` and is their only source of truth: clients (the
Dashboard, the AI through MCP) use its API, never its database. Gateway base:
`/api/food`; the contract is `services/food/api/openapi.yaml`; `/health` and
`/ready` are runtime probes.

The owner uses Food through the Dashboard's Food workspace (`/food`: the
shopping list, made for the phone in the shop, and the pantry), and Home
shows the list's size and food expiring today or tomorrow; see
[Dashboard](../frontend/dashboard.md).

**Groceries go to the list; other purchases stay Tasks.** Food, drinks,
household, and drugstore items belong on the shopping list; buying a lamp or
a birthday present stays a Task. The API serves the AI's routing (see
[AI capture](#ai-capture)) with bulk adds that merge, and stores given by name.

Every write needs `X-Life-OS-Actor` (see [Activity](activity.md#the-actor-gate))
and takes an `Idempotency-Key` (see [Writes](#writes-actors-and-idempotency)).
Errors use the shared envelope `{"error": {"code", "message"},
"request_id"}`; the codes below are the `code`.

## Names

Names are kept as written (without surrounding whitespace) and compared by
their normalised form, `normalizedName`: Unicode NFKC, lower case, "ё" as "е",
runs of whitespace as one space, and surrounding punctuation stripped.
"Молоко", "  молоко!" and "МОЛОКО" are the same item, and "Ёжик" is "ежик".
There is no stemming: "молоко" and "молока", or "egg" and "eggs", stay
different. A name that normalises to nothing ("!!!") is refused (400
`invalid_request`).

## Shopping list

A shopping item has a `name`, an optional `amount` (free text such as "2 l",
"6", or "500 g"), an optional `note`, an `aisle`, an optional store
(`storeId`, with its `storeName`), a `state` (`needed` or `bought`, with
`boughtAt`), a `source` (`manual`, or `capture` for an item that came from a
capture, with a `sourceRef` such as the Inbox item's id), and who created and
last changed it (`createdBy`, `updatedBy`: `{kind, name}` as the actor header
said).

**One needed item per name.** The list holds at most one needed item per
normalised name; a partial unique index enforces it. Adding a name that is
already needed merges into that item instead of adding a second one, and the
response marks the result `merged: true`:

- an `amount`, `note`, or store the add gives replaces the item's; what it
  does not give stays;
- an `aisle` the add gives replaces the item's aisle; otherwise the item keeps
  its aisle;
- the name stays as first written, and so do `source` and `sourceRef`.

The add is an upsert on that index, so two adds of one name at the same time
leave one item, the second merged into the first. A bought item takes no
merges: adding its name again puts a new needed item on the list.

`POST /v1/shopping-items` adds 1 to 50 items, `{"items": [...]}`, in one
transaction: all or none (one invalid item refuses the request, its error
message naming `items[i]`). The response lists `{item, merged}` for each, in
the request's order; two items of one request with the same name merge too.
Each item may name its store by `storeId` or by `storeName`, not both.

`GET /v1/shopping-items` lists the needed items first, by aisle (in the order
below), then by normalised name, then the bought items, the most recently
bought first. `state`, `storeId`, and `aisle` filter; `limit` is 1 to 200
(default 100), and when more items match, `nextCursor` is set: pass it back as
`cursor`, with the same filters, for the next page. A malformed cursor is 400
`invalid_cursor`.

- `GET`/`PATCH`/`DELETE /v1/shopping-items/{id}`. `PATCH` changes only the
  fields present (`name`, `amount`, `note`, `aisle`, `storeId`, `storeName`);
  `null` clears `amount`, `note`, or the store (`storeId: null`). A new name
  gets a new normalised name; when another needed item has it, the rename is
  refused (409 `already_on_list`). An update that changes nothing records
  nothing.
- `POST /v1/shopping-items/{id}/bought` sets `state: bought` and `boughtAt`.
- `POST /v1/shopping-items/{id}/needed` puts a bought item back. When an item
  with its name was added meanwhile, the two merge: the needed item stays,
  takes the bought item's amount, note, and store where it has none, and the
  bought item is deleted; the response is the needed item with `merged: true`.
- `POST /v1/shopping-items/clear-bought` deletes the bought items, or with
  `{"boughtBefore": "<instant>"}` only those bought before it, and returns
  `{deleted}`.

Marking a bought item bought, or a needed item needed, returns it unchanged
and records nothing.

## Aisles and aisle rules

The aisles are fixed, in the order the list shows them: `produce`, `bakery`,
`dairy`, `meat_fish`, `frozen`, `pantry`, `drinks`, `snacks`, `household`,
`drugstore`, `other`.

An item added without an `aisle` is placed by the **aisle rules**
(`aisleSetBy: rule`); an aisle the request names always wins
(`aisleSetBy: manual`). A rule is a normalised `keyword`, an `aisle`, and how
it matches:

- `word`: the keyword matches whole words of the name ("milk" matches "oat
  milk", not "buttermilk"; "tea" does not match "steak"). Russian and English
  keywords are words.
- `substring`: the keyword matches anywhere, also inside a word, so German
  compounds work ("milch" matches "Vollmilch", "öl" matches "Olivenöl").

When several rules match, the **longest keyword wins**: "Milchschokolade" is
snacks ("schokolade") rather than dairy ("milch"), "Schweinebauch" is meat
("schwein") rather than drinks ("wein"). Between equally long keywords a whole
word beats a substring, then the keyword that sorts first wins. No match means
`other`. Rules place items when they are added; a rule created or deleted
later does not move items already on the list. When the rules chose an item's
aisle, renaming it lets them choose again; an aisle set by a request stays.

Food starts with about 190 built-in rules (`createdBy` `system:food`), seeded
by its migration: common grocery, household, and drugstore words in English
and Russian (`word`) and German (`substring`), with a few longer ones so the
longest match picks the right aisle ("orangensaft" is a drink, not produce;
"подсолнечное" makes "масло подсолнечное" oil, not butter). Because there is
no stemming, the keywords are the forms a list usually has: plurals for
countable things ("eggs", "apples", "яйца", "помидоры"). The owner can delete
any rule and add their own:

- `GET /v1/aisle-rules` (`aisle` filters), ordered by keyword;
- `POST /v1/aisle-rules` `{keyword, aisle, match}` (`match` defaults to
  `word`); a keyword is unique as normalised (409 `aisle_rule_exists`);
- `DELETE /v1/aisle-rules/{id}`.

## Stores

A store has a `name` (unique as normalised, so case and spacing do not make a
second store; 409 `store_exists`), a `sortOrder`, and `archived`/`archivedAt`.
There is one list for all stores: a store is an optional tag on an item, and
`GET /v1/shopping-items?storeId=…` shows one store's items.

- `GET /v1/stores` lists the active stores by `sortOrder`, then name
  (`includeArchived=true` adds archived ones); `GET /v1/stores/{id}`.
- `POST /v1/stores` `{name, sortOrder}`; without `sortOrder` the store goes
  after the others.
- `PATCH /v1/stores/{id}` renames (`name`), reorders (`sortOrder`), or
  archives (`archived: true`; `false` restores). Stores are never deleted, so
  items keep theirs.

An item names its store by `storeId` or by `storeName`. A name matches an
existing store case-insensitively (as normalised); an unknown name creates the
store, once, also when several items of one request or two requests at once
name it, and the store gets the name as the first of them wrote it. An
archived store named by id is refused (409 `store_archived`); named by name it
is restored. An unknown `storeId` is 400 `unknown_store`.

## Pantry

A pantry item has a `name`, an optional `amount`, a `location` (`fridge`,
`freezer`, `pantry`, or `other`), optional `expiresOn` and `openedOn` (plain
dates), a `note`, a `state` (`in_stock`, `used_up`, or `thrown_away`, with
`endedAt` for an ended one), `source`/`sourceRef`, an optional
`fromShoppingItemId` (the shopping item it was bought as; it must exist when
the pantry item is added, 400 `unknown_shopping_item`, and is kept after the
shopping item is cleared), and the actors. Pantry items never merge: two
cartons of milk may expire on different days.

- `POST /v1/pantry-items` adds 1 to 50 items, all or none. An item without
  `location` gets the usual place of the aisle the rules give its name: frozen
  food the freezer, dairy, meat, and fish the fridge, bakery, pantry, drinks,
  and snacks the pantry, anything else `other`.
- `GET /v1/pantry-items` lists in-stock items first, soonest `expiresOn`
  first (items without one last), then by normalised name, then ended items,
  the most recently ended first. `state` and `location` filter, and paging
  works as for the shopping list. `expiringWithinDays=N` (0 to 366) keeps
  in-stock items that expire on or before today + N days, today being the
  owner's (`LIFE_OS_TIMEZONE`): at 00:30 on 11 October in Berlin, still 10
  October in UTC, `expiringWithinDays=1` includes items that expire on 12
  October. Already expired items are included; combined with an ended `state`
  it is 400 `invalid_request`.
- `GET`/`PATCH`/`DELETE /v1/pantry-items/{id}`. `PATCH` changes only the fields
  present; `null` clears `amount`, `expiresOn`, `openedOn`, or `note`.
- `POST /v1/pantry-items/{id}/used-up`, `/thrown-away`, and `/restock` (back to
  `in_stock`). Ended items are kept for history; marking an ended item ended
  the other way keeps its `endedAt`. `DELETE` removes an item and its history.

## Writes: actors and idempotency

Every write must name its actor in `X-Life-OS-Actor` (`user`, `agent:<name>`,
`system:<name>`, or `dev:<name>`); without one it is 400 `actor_required`
(`pkg/activity.RequireActor`). The actor is stored on the rows it writes
(`createdBy`, `updatedBy`) and on its Activity events.

Every write takes an `Idempotency-Key` header (1 to 200 characters), with the
same meaning as for Tasks and Calendar creates, but for every write: the
write's response is stored with the key in the write's own transaction, and a
repeat of the same request (method, path with query, and body) returns that
response byte for byte, changes nothing, and records nothing. A repeat that
arrives while the first is still running waits for it. The same key with
another request is 409 `idempotency_conflict`. A refused write stores nothing,
so its key stays free. Keys are kept for 30 days (`idempotency_keys`, pruned
at most hourly).

## AI capture

Inbox captures reach Food through MCP (P15-03; see
[MCP and AI](mcp.md#food)). The processor routes groceries written in English,
Russian, or German to the shopping list and the pantry, and keeps other
purchases as Tasks:

- "Buy milk and 6 eggs", "купить хлеб и сыр в Lidl", "Zahnpasta und Duschgel
  bei dm kaufen", and "buy milk tomorrow" add items
  (`food_add_shopping_items`), named as the owner wrote them and untranslated,
  with the quantity as `amount` and the store by name. "Buy a flash drive
  tomorrow" stays a Task, and so does an explicit "Create a task: buy milk";
  "buy milk and call mom" is split into an item and a Task. When it is
  unclear whether something is a grocery, the capture goes to Human Review.
- "I bought the milk" marks the one needed item that matches bought
  (`food_mark_bought`), also in another language ("Молоко"). With no such
  item the one matching open Task is completed, with none of either nothing
  happens, and several matches go to Human Review.
- "Йогурт в холодильнике до пятницы" adds a pantry item that expires the
  coming Friday (`food_add_pantry_items`). "Молоко закончилось" marks the one
  matching in-stock item used up (`food_update_pantry_item`), and puts milk
  on the list when the capture says to buy more.

Items from a capture have `source: capture` and the Inbox item's id as
`sourceRef`, and their rows and events name `agent:codex-inbox-processor` as
the actor. Every write carries an `Idempotency-Key` derived from the capture
and the write, so a retried capture changes nothing twice; the merge rule (one
needed item per name) is the second safety net.

## Activity

Each change made through the API records one event (source `food`) with the
request's actor, correlation, and causation; a change that changes nothing
records nothing, and an idempotent replay records nothing. Notes are never
recorded. A creation or deletion has an id derived from the entity
(`activity.EventID`); repeatable changes get random ids.

| Type | Subject | Summary, for example |
| --- | --- | --- |
| `food.shopping_item.added` | `food_shopping_item` | Added “Vollmilch” (2 l, Lidl) to the shopping list |
| `food.shopping_item.merged` | `food_shopping_item` | “Vollmilch” was already on the shopping list: amount updated |
| `food.shopping_item.updated` | `food_shopping_item` | Shopping item “Milch” updated: name, aisle |
| `food.shopping_item.bought` | `food_shopping_item` | Bought “Milch” |
| `food.shopping_item.reopened` | `food_shopping_item` (the needed item) | “Milch” is needed again (a merge adds ", merged into the item already on the list" and `data.mergedFromId`) |
| `food.shopping_item.deleted` | `food_shopping_item` | Removed “Brot” from the shopping list |
| `food.shopping_items.cleared` | `food_shopping_list` / `bought` | Cleared 3 bought items from the shopping list |
| `food.store.created` / `.updated` / `.archived` / `.restored` | `food_store` | Store “Lidl” created (also when an item's `storeName` creates it) |
| `food.aisle_rule.created` / `.deleted` | `food_aisle_rule` | Aisle rule created: “kerze” → household (also inside words) |
| `food.pantry_item.added` / `.updated` / `.used_up` / `.thrown_away` / `.restocked` / `.deleted` | `food_pantry_item` | Added “Joghurt” to the pantry (fridge, expires 2026-10-14) |

`data` holds the item's name, aisle or location, state, source, amount, store,
`sourceRef`, expiry date, and for updates and merges the changed fields
(`changes`). See [Activity](activity.md).

## Storage and operations

- Database: `food_db` / `food_user`, password `LIFE_OS_FOOD_DB_PASSWORD` in
  `.env`; migrations in `services/food/db/migrations` (`food-migrate`). A fresh
  PostgreSQL volume provisions it; for an existing deployment see
  [Operations](../operations/deployment.md#adding-the-food-service).
- Configuration: `ACTIVITY_SERVICE_URL` (compose: `http://activity:8080`;
  empty records nothing) and `LIFE_OS_TIMEZONE` (the owner's "today" for
  `expiringWithinDays`; an unknown name falls back to UTC, logged).
- The Dashboard and MCP reach Food at `FOOD_SERVICE_URL` (compose:
  `http://food:8080`), and the browser at `/api/food`; Status probes its
  `/ready` ("Food").

Tests: `internal/domain` has unit tests for normalisation (NFKC, case, ё,
punctuation, whitespace) and for the built-in rules, read from the migration
(RU, DE, and EN examples, German compounds, the longest match, word
boundaries, tie-breaks). The integration tests run against
`FOOD_TEST_DATABASE_URL` (a throwaway Postgres; each test creates and drops
its own schema and applies every migration): merges on add (also within one
request, and eight concurrent adds of one name leaving one row), stores by
name created once, bought and needed with the merge on reopen, clearing
bought items, renames and their conflicts, order and paging, the aisle rules
API, the pantry lifecycle and `expiringWithinDays` at a timezone day
boundary, idempotent replays and conflicts (also concurrent requests with one
key), the actor gate, the Activity events, and migration 000002 down and up.
