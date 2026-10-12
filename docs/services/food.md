# Food service

Food keeps the owner's groceries: one shopping list grouped by aisle, where an
item may be tagged with the store to buy it at; the stores; the aisle rules
that place new items; the pantry, what is at home and when it expires; the
staples, the things the owner always wants at home, which go back on the
list by themselves when they run low; and the recipes and the weekly meal
plan, from which missing ingredients go on the list. It
keeps them in `food_db` and is their only source of truth: clients (the
Dashboard, the AI through MCP) use its API, never its database. Gateway base:
`/api/food`; the contract is `services/food/api/openapi.yaml`; `/health` and
`/ready` are runtime probes.

The owner uses Food through the Dashboard's Food workspace (`/food`: the
shopping list, made for the phone in the shop, from where bought items are
put away into the pantry; the pantry with its staples; the recipes; and the
meal plan), and Home shows the list's size, food expiring today or tomorrow,
today's planned meals, and what is running low; see
[Dashboard](../frontend/dashboard.md).
Once a day Food also tells the owner which pantry food expires soon (see
[Expiry reminders](#expiry-reminders)).

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
`boughtAt`), a `source` (`manual`; `capture` for an item that came from a
capture, with a `sourceRef` such as the Inbox item's id; or `recipe` for a
recipe's missing ingredient, with the recipe's id), and who created and
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
- `POST /v1/shopping-items/put-away` puts 1 to 50 bought items away, `{"items":
  [{"id", "location", "expiresOn", "amount"}]}`, in one transaction: all or
  none. Each becomes an in-stock [pantry](#pantry) item with the shopping
  item's name, its amount (or the `amount` given; empty means none),
  `fromShoppingItemId`, and its `source` and `sourceRef`, in the `location`
  given or else the usual place of the item's aisle (as for a pantry item
  added without one), with the `expiresOn` given; and the shopping item
  leaves the list (its note does not come along). An item that is still
  needed refuses the request (409 `not_bought`), an unknown id is 404
  `not_found` (also a second put-away of one item), and an id named twice is
  400 `invalid_request`, each message naming `items[i]`. The response lists
  the new pantry items, `{items}`, in the request's order.

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
  the most recently ended first. `state`, `location`, and `runningLow`
  (`true`: the items flagged as running low) filter, and paging
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
  These answer with the item plus `restock`: what went on the shopping list
  by itself when the item's name is a staple (see
  [Staples and running low](#staples-and-running-low)); absent otherwise.
- An in-stock item may be flagged as running low (`runningLow`,
  `runningLowAt`); ending or restocking it clears the flag, and so does more
  of its name coming home (added, or put away from the list).

## Staples and running low

A staple is a name the owner always wants at home ("Milch", "Eier"), unique
by normalised name and independent of pantry rows: Milch stays a staple when
none is at home. It has an optional `amount`, store, and `aisle`, which the
shopping list gets (no aisle lets the rules choose; an archived store is left
out). The staple list says for each whether an item of its name is needed on
the list (`onList`), in stock (`inStock`), and flagged as running low
(`runningLow`).

- `GET /v1/staples` lists them by name; `POST /v1/staples` adds one (`name`,
  optional `amount`, `storeId` or `storeName`, `aisle`; another of the same
  normalised name is 409 `staple_exists`). "Mark as staple" from a pantry or
  shopping item is a `POST` with its name.
- `GET`/`PATCH`/`DELETE /v1/staples/{id}`. `PATCH` changes only the fields
  present; `null` clears `amount`, `storeId`, or `aisle`. Deleting a staple
  leaves the list alone.
- `POST /v1/pantry-items/{id}/running-low` flags an in-stock item (an ended
  one is 409 `not_in_stock`); `DELETE` on the same path clears the flag and
  leaves the list alone. An item already flagged keeps its flag and records
  nothing, but its staple still goes on the list if none is needed there
  (after the listed one was bought, say).
- `POST /v1/staples/running-low` `{name, addToList}` is the same by name, for
  "we're almost out of milk" when the pantry may not track it: every in-stock
  item of the name is flagged, the staple (if the name is one) goes on the
  list, and the answer has `changed` (whether anything was flagged or
  listed), `staple`, `pantryItems`, and `restock`. A name that is neither a
  staple nor at home changes nothing (`changed: false`, `restock: null`).

**Restock.** When an item whose normalised name is a staple is marked
running low, used up, or thrown away, Food puts the staple on the shopping
list in the same transaction, through the same merge-aware add as any other
add: the staple's name, amount, store, and aisle, `source: staple`, and the
staple's id as `sourceRef`. If an item of that name is already needed nothing
changes. The answer's `restock` says what happened:

| `status` | Meaning | `message`, for example |
| --- | --- | --- |
| `added` | A new needed item | Added “Milch” to the list: running low (staple) |
| `merged` | Another request added it meanwhile; merged into it | “Milch” is already on the list |
| `on_list` | Already needed; nothing changed | “Milch” is already on the list |

with `reason` (`running_low`, `used_up`, `thrown_away`), `stapleId`, and the
`shoppingItem`. Another item is only flagged, unless `addToList: true` puts
its own name on the list (`source: manual`, `stapleId: null`, message
without "(staple)"); used up or thrown away it only ends. Names match by
their exact normalised form ("Milch" is not "Vollmilch").

## Recipes

A recipe has a `title` (unique as normalised, like item names: 409
`recipe_exists`, so a title names one recipe), optional `servings` (1 to
100), `ingredients` (an ordered list, at most 100, of `name`, optional
`amount` (free text, 50 characters), and optional `note`; each with its
`normalizedName`), `steps` (plain text, one step per line; no markup),
`tags` (at most 20, kept normalised and without duplicates, in the order
given), an optional `sourceUrl` (http or https), a `note`, its cooking
`methods` (see below), and the actors.

- `GET /v1/recipes` lists recipes by normalised title. `q` keeps those whose
  normalised title contains every word of it, also inside a word ("bolo"
  finds "Pasta Bolognese"); `tag` keeps those with the tag; `ingredient`
  keeps those with an ingredient whose normalised name contains it ("hack"
  finds "Hackfleisch"). Filters combine; paging as for the shopping list.
- `POST /v1/recipes` (201), `GET`/`PATCH`/`DELETE /v1/recipes/{id}`. `PATCH`
  changes only the fields present; `null` clears `servings`, `steps`,
  `sourceUrl`, or `note`; `ingredients` and `tags` replace the whole list. An
  update that changes nothing records nothing.
- Deleting a recipe keeps the meals planned with it, as plain text with its
  title.

**Cooking methods.** `methods` lists how a recipe is cooked: `air_fryer`,
`oven`, `stovetop`, `slow_cooker`, `grill`, `microwave`, `no_cook` (in that
order). Until they are set, Food infers them whenever it reads the recipe
(`methodsInferred: true`) from the title, tags, and steps by keyword rules in
English, German, and Russian: "air fryer", "Airfryer", "Heißluftfritteuse",
"аэрогриль" are the air fryer; "oven", "bake", "Backofen", "Auflauf",
"духовка", "запекать" the oven; "pan", "skillet", "Pfanne", "anbraten",
"kochen", "сковорода", "варить" the stove; "slow cooker", "Schongarer",
"мультиварка"; "grill", "BBQ", "мангал"; "microwave", "Mikrowelle", "СВЧ";
"no cook", "ohne Kochen", "без варки". A device's words take the verb next
to them ("Bake in the air fryer", "Запекать в аэрогриле" is only the air
fryer). So imported and older recipes need nothing. `methods` on create, or
on `PATCH`, sets them (`methodsInferred: false`, an empty list means none);
`PATCH` with `methods: null` lets Food infer them again.

**Importing.** Food fetches no page itself: recipes from links and pasted
captions come through the [recipe-import](recipe-import.md) integration,
which reads the page and saves the recipe with `POST /v1/recipes` as
`system:recipe-import`, the link as its `sourceUrl`, and the page's
description and image link in its `note`.

**Finding a recipe from words** (as the AI does for "plan lasagne for
Friday"): normalise the words and compare them with `normalizedTitle`. A
recipe whose normalised title equals them is the one (titles are unique);
otherwise `GET /v1/recipes?q=<words>` and, when exactly one recipe matches,
that one; when several match, ask (or plan the words as free text); when none
does, plan them as free text (`text`), which the owner can turn into a recipe
later.

## Preferences

`GET /v1/preferences` and `PUT /v1/preferences` (it replaces them all): the
preferred cooking `methods`, the most preferred first (the air fryer to
start with, migration 000006); `dislikes`, ingredients never to suggest
(names as written, at most 50, kept once per normalised name); and free
`dietaryNotes` (1000 characters) for whoever suggests recipes. A request
that changes nothing records nothing.

## Suggestions

`GET /v1/suggestions?limit=10&mealSlot=dinner` ranks the saved recipes for
the owner's today and explains every score: each `reason` has a `kind`, its
`points`, and a `text`, and the `score` is their sum.

| Reason | Points | Text, for example |
| --- | --- | --- |
| `coverage` | up to 40, by the share of its ingredients in stock | You have 4 of its 5 ingredients |
| `expiring` | 12 per in-stock item it uses that expires today through in 3 days (at most 3) | Hähnchenbrust expires tomorrow, Sahne expires in 2 days |
| `method` | 10 when it is cooked one of the preferred ways | Air fryer, your preferred way |
| `habit_planned` | planned in the last 60 days: once 4, twice 7, three times or more 9 | You had it 3 times in the last 60 days |
| `habit_bought` | up to 8, by the share of its ingredients bought or put away in the last 60 days | You bought 3 of its ingredients lately |
| `recent` | −10 when it is planned within 3 days before or after today | Planned 2 days ago; Already planned for tomorrow |
| `slot` | with `mealSlot`: +5 when it was planned for that meal before or is tagged for it ("dinner", "Abendessen", "ужин"); −10 when tagged only for another meal | Fits dinner; Usually breakfast |

Ingredients are compared with the pantry **word by word**, not only by the
exact normalised name: every word of the shorter name must match a word of
the other, where words match when they are equal, one is the other's plural
("Eier"/"Ei", "Tomaten"/"Tomate", "eggs"/"egg"), one of at least five letters
starts the other ("Hähnchen"/"Hähnchenbrust"), a compound ends with the
other ("Vollmilch" covers "Milch"), or, in Cyrillic, they share a stem and
differ in short endings ("молоко"/"молока", "яйцо"/"яйца"). "Egg" does not
match "eggplant", "tea" not "steak". It is a heuristic; `have` says which
pantry item covers which ingredient, so the owner sees it. Water, salt,
pepper, and cooking oil are assumed at home (`basics`) and not counted.
`missing` lists what to buy with the recipe's amounts, and `expiring` the
covered items that expire within 3 days, soonest first. A recipe with a
disliked ingredient (or a disliked word in its title), compared the same
way, is left out and listed in `excluded` with the dislike. Ties go to fewer
missing ingredients, then the title.

## Recipe ideas

Ideas are recipe drafts that are not recipes yet, each with why it is
suggested (`reason`), for the owner to save or dismiss. Food stores them;
something else writes them: the processor's recipe ideas job (see
[AI ideas](#ai-ideas)). An idea has the recipe fields (title,
servings, ingredients, steps, tags, methods), a `source` (`ai`, or `web`
with the page as `sourceUrl`), the `reason`, a `state` (`new`, `saved`,
`dismissed`), and, once saved, the `recipeId`.

- `POST /v1/ideas` (201) writes one. Only an agent, a Life OS component, or
  a development tool may (`agent:`, `system:`, `dev:` actors; `user` is 403
  `actor_not_allowed`). A new idea with the same normalised title is 409
  `idea_exists`, a saved recipe with it 409 `recipe_exists`. Methods not
  given are inferred.
- `GET /v1/ideas?state=new` (the default; `saved`, `dismissed`), newest
  first, `limit` up to 100; `GET /v1/ideas/{id}`.
- `POST /v1/ideas/{id}/save` creates the recipe (201): the idea's fields,
  its link, the reason as the note, and its methods when they were given
  (inferred ones stay inferred). An idea already saved answers 200 with its
  recipe; a title taken meanwhile is 409 `recipe_exists`. A dismissed idea
  can still be saved.
- `POST /v1/ideas/{id}/dismiss`; again changes nothing; a saved idea is 409
  `idea_saved`.

### AI ideas

The Inbox processor writes ideas (P15-09; see
[MCP and AI](mcp.md#recipe-ideas)): once a day at `LIFE_OS_IDEAS_TIME`
(16:00 by default) when the pantry has food in stock, and when the owner
taps **Get ideas** (at most one run every 10 minutes and six a day). Each run
reads the preferences, the in-stock pantry, the top suggestions, the ideas,
and the recent meal plan through MCP, asks Codex for up to three home
recipes that use the food expiring within 3 days first, name in-stock
ingredients as the pantry does (so coverage matches), avoid every dislike,
and prefer the preferred methods (the air fryer first), checks them, and
writes each with `POST /v1/ideas` as `agent:codex-recipe-ideas`, with an
Idempotency-Key from the run and the title; a 409 is skipped. Web search is
off unless `LIFE_OS_IDEAS_WEB_SEARCH` turns it on; then ideas found online
are `source: web` with their page, their steps summarized, not copied. The
job never saves or dismisses ideas.

## Meal plan

The meal plan holds at most one meal per date and slot (`breakfast`,
`lunch`, `dinner`). A meal is a recipe (`recipeId`) or free text; its `title`
is the recipe's title (kept in step when the recipe is renamed) or the text.
It may have `servings` and a `note`. Dates are plain dates, the owner's.

- `GET /v1/meal-plan?from=&to=` lists the meals from one date through
  another (at most 31 days), by date, then slot. Empty slots are not listed.
- `PUT /v1/meal-plan/{date}/{slot}` sets a slot, `{"recipeId"}` or
  `{"text"}` (exactly one), with optional `servings` and `note`, replacing
  what it held (the entry keeps its id); an unknown recipe is 400
  `unknown_recipe`. Setting what the slot already holds changes nothing.
- `DELETE /v1/meal-plan/{date}/{slot}` clears it; nothing planned there is
  404 `not_found`.

## Missing ingredients

`POST /v1/shopping-items/from-recipes` puts the ingredients of recipes on the
list that are neither at home nor already needed. It takes the recipes
directly (`{"recipeIds": [...]}`, 1 to 50) or every recipe planned in a date
range (`{"from", "to"}`, at most 31 days; a recipe planned twice counts
once), and `dryRun`.

- **One line per ingredient.** Ingredients of several recipes with the same
  normalised name are one line: its name as the first recipe writes it, the
  recipes that need it, and their amounts joined in order, "200 g + 1 Pck."
  (free text is never added up; empty amounts are left out; shortened to 50
  characters).
- **Skipped.** An ingredient with an in-stock pantry item of the same
  normalised name is `in_pantry` (with that `pantryItem`); one with a needed
  shopping item of that name is `on_list` (with that `shoppingItem`). Amounts
  are not compared: having some is having it.
- **Added.** The rest are added like `POST /v1/shopping-items` (aisle rules,
  merge), with the joined amount, `source: recipe`, and `sourceRef` the first
  recipe's id: `added`, or `merged` if a needed item of the name appeared
  meanwhile; each with its `shoppingItem`.
- **Dry run.** With `dryRun: true` nothing changes and the statuses say what
  would happen (`added` for what would be added, without a `shoppingItem`).
- The response is `{dryRun, items}`, the ingredients in recipe order. An
  unknown recipe is 404 `not_found` (naming `recipeIds[i]`); both or neither
  of `recipeIds` and the range is 400. All or nothing, and idempotent with
  its `Idempotency-Key`.

## Expiry reminders

Once a day Food tells the owner, through [Notifications](notifications.md)
(`pkg/notify`, the one notification path), which in-stock pantry food expires
soon. There are no quiet hours.

| When | Content | Importance, category | Link | Dedupe key |
| --- | --- | --- | --- | --- |
| Daily at `FOOD_EXPIRY_TIME` (08:00) on the owner's wall clock (`LIFE_OS_TIMEZONE`), when anything in stock expires that day or the next. | Titled "Food expiring today", or "Food expiring tomorrow" when nothing expires on the day itself. One line per item, that day's first: "Today: Milch", "Tomorrow: Joghurt (2)" (two items of one name expiring that day share a line). | `medium` when something expires that day, else `low`; `food.expiry` | `/food?tab=pantry` | `food:expiry:<local date>` |

- **What it names.** The pantry is read when the reminder is sent, so food
  used up, thrown away, or moved to a later date before then is not named.
  Food that expired before the day is not named again every morning: the
  Pantry tab shows it in red. Items without an expiry date are never named.
- **Nothing to say.** A day with nothing expiring today or tomorrow sends
  nothing (recorded as empty, never as missed).
- **Lists.** At most 10 lines, then "and 3 more"; names on one line,
  shortened to 120 characters. A reminder sent late on another day names the
  days ("Sat 10 Oct: Milch").
- **Wall clock.** 08:00 stays 08:00 across daylight saving changes; a time
  skipped when the clocks go forward is the same time after the jump, one that
  occurs twice is the first, as for Tasks.

**Delivery.** A goroutine in the Food process (`internal/notifications`) runs
every `FOOD_NOTIFY_INTERVAL_SECONDS` (30) and handles what came due in `(last
run, now]`. It follows [Tasks' notification delivery](tasks.md#notifications)
without sharing its code:

- **Exactly once.** Notifications stores a dedupe key once. Food also keeps its
  cursor (`food_notification_dispatch.last_run_at`) and every handled key
  (`food_notification_log`: sent, missed, failed, or empty; pruned after 30
  days), so restarts, overlapping runs, and a cursor set back neither repeat
  nor skip a reminder. One process runs at a time: a run holds a lease on the
  state row (two minutes, at least four intervals).
- **Failures.** When Notifications is unreachable or answers 5xx, the run stops
  and keeps the cursor just before the reminder, so the next run retries it.
  A reminder Notifications refuses (4xx) is recorded as failed, logged, and
  not retried. Either error is kept as `lastError`.
- **Missed while down.** The first run after Food was down handles what came
  due since the last run, at most 7 days back. A reminder up to
  `FOOD_NOTIFY_GRACE_MINUTES` (120) late is sent, the pantry read when it is
  sent; an older one is not notified but recorded in Activity as
  `food.notification.missed`. The very first run after deployment only starts
  the cursor.
- **Off without Notifications.** With `NOTIFICATIONS_SERVICE_URL` unset the
  dispatcher does not run (logged at startup).

`food.notification.missed` has actor `system:food`, outcome `failed`,
`occurredAt` the time the reminder was due, an id derived from the dedupe key
(recorded once), subject `food_notification` with the dedupe key as id, a
summary such as "Expiry reminder due Sun 11 Oct 2026, 08:00 CEST was missed:
Food was not running", and `data` with `kind` (`expiry`), `day`, `dueAt`, and
`dedupeKey`.

`GET /v1/notifications/status` reports the delivery's health: `enabled`,
`intervalSeconds`, `graceMinutes`, `timeZone`, `expiryTime`, `handledThrough`
(the cursor), `lastRunAt`, `lastSuccessAt`, `lastError` with `lastErrorAt`
(current while later than `lastSuccessAt`), and `lastSentAt`. The Dashboard
Status page shows it as the Food reminders part of the Notifications check
(see [Dashboard](../frontend/dashboard.md)), so a reminder sender that stops
or keeps failing reaches the owner through the monitor.

| Variable | Default | Meaning |
| --- | --- | --- |
| `NOTIFICATIONS_SERVICE_URL` | empty | The Notifications service (compose: `http://notifications:8080`); empty turns expiry reminders off. |
| `FOOD_EXPIRY_TIME` | `08:00` | When the daily reminder is due, HH:MM in `LIFE_OS_TIMEZONE` (compose passes `LIFE_OS_FOOD_EXPIRY_TIME`). |
| `FOOD_NOTIFY_INTERVAL_SECONDS` | `30` | How often due reminders are looked for, 5 to 300. |
| `FOOD_NOTIFY_GRACE_MINUTES` | `120` | How late a reminder is still sent after Food was down, 10 to 1440; older ones are recorded as missed. |

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
- "Pasta Bolognese for dinner on Wednesday", "на ужин в среду паста", and
  "Mittwoch Abendessen: Pasta" plan a meal (`food_plan_meal`, P15-05) in the
  slot the meal word names, on the day resolved in the owner's timezone. The
  words find the recipe as [Recipes](#recipes) describes: the exact title,
  else the only recipe a `q` search finds; several go to Human Review with
  the recipes as candidates, and none plans the words as text ("В пятницу на
  обед суп" is the text "суп"). A meal without a meal word goes to Human
  Review. "Cancel dinner on Friday" clears the slot (`food_clear_meal`; an
  empty slot is fine).
- "Add the ingredients for the bolognese to the list" and "купи всё для
  борща" add the missing ingredients of the recipe the words find
  (`food_add_missing_ingredients`; with no such recipe the capture goes to
  Human Review: "No recipe is called “борщ”. Add the recipe on the Food
  page, or say which items to buy."), and "buy what
  I need for this week's meals" those of the meals planned Monday to Sunday
  of the capture's week. "Buy pasta" stays a list item. The AI never creates
  or edits recipes.

**Staples and running low** (P15-08; see [MCP and AI](mcp.md#staples-and-running-low)). The processor maps the owner's words onto the staple operations; the phrasing is the AI's, Food only offers them:

- "We're almost out of milk", "мясо заканчивается", "почти закончился рис", "fast keine Milch mehr": `food_mark_running_low`. The processor matches the words, in any of the three languages, to the one staple or in-stock pantry name Food knows ("мясо" finds the staple "Fleisch", "рис" the pantry's "Reis"; several names go to Human Review) and sends `POST /v1/staples/running-low` with that name, since Food compares names only by their exact normalised form. A staple goes on the list by itself (`source: staple`); another item is only flagged, unless the capture also says to buy it ("заканчивается, купи"), which sends `addToList: true`. A name Food does not know is sent as the owner wrote it and changes nothing (`changed: false`, `restock: null`) unless `addToList` puts it on the list.
- "Milk is a staple", "молоко всегда должно быть дома", "Milch immer da haben", "Kaffee sollte ich immer zu Hause haben": `POST /v1/staples` with the name as written and the amount and store when said. A name already a staple (409 `staple_exists`) counts as done. Nothing goes on the list now.
- "Remove milk from staples", "молоко больше не нужно держать дома": the one staple whose name the words match is deleted (found in `GET /v1/staples` by `normalizedName`, then `DELETE /v1/staples/{id}`); none matching removes nothing, several go to Human Review.
- "The milk is finished, buy more" stays `used-up` on the matching pantry item plus a list add. When the item is a staple, the answer's `restock` says it is on the list already (`added`, `merged`, or `on_list`), and the processor leaves it out of the capture's own add, also when the capture names it in another language ("молоко" for the staple "Milch").
- "Milk isn't low any more" is not routed by the AI: the flag is cleared on the Food page, or by more of the name coming home.

Every one of these takes an `Idempotency-Key` like the other writes, and the items a staple puts on the list have `source: staple`, not `capture`.

**Recipe ideas (for the AI agent, part 2 of P15-09).** Food stores ideas and
shows them in the Dashboard's "What can I cook?"; the AI writes them. A
generator job should:

1. Read what it needs: `GET /v1/preferences` (preferred `methods`, the
   `dislikes`, the `dietaryNotes`), `GET /v1/pantry-items?state=in_stock`
   (and `expiringWithinDays=3` for what to use first), `GET
   /v1/suggestions?limit=10` (what the saved recipes already cover, so it
   suggests something else), `GET /v1/ideas` (the waiting ones, not to
   repeat), and `GET /v1/recipes?q=` to avoid titles that are saved.
2. Write each idea with `POST /v1/ideas` as `agent:<name>` and an
   `Idempotency-Key` derived from the run and the title: `title`,
   `ingredients` (name and amount, in the owner's language where it can,
   using the pantry's own names for what is at home so coverage matches),
   `steps` one per line, `tags`, `methods` (the air fryer when it suits; or
   leave them out and Food infers them from the steps), `servings`, and a
   one-sentence `reason` that names what it uses ("Uses the Hähnchen that
   expires tomorrow and the Paprika you have, in the air fryer"). `source:
   ai` for its own recipes; `source: web` with `sourceUrl` for one found on
   a site (the owner then sees "Found on chefkoch.de").
3. Respect the rules: never an idea with a disliked ingredient; prefer the
   preferred methods; prefer what expires within 3 days; a few ideas a day
   at most (each new title waits until saved or dismissed: 409
   `idea_exists`); 409 `recipe_exists` means it is saved already, skip it.
   It never saves or dismisses ideas itself; the owner does.

Items from a capture have `source: capture` and the Inbox item's id as
`sourceRef` (missing ingredients have `source: recipe`, as always), and their
rows and events name `agent:codex-inbox-processor` as the actor. Every write carries an `Idempotency-Key` derived from the capture
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
| `food.shopping_item.put_away` | `food_shopping_item` | Put away “Milch” in the fridge (`data.pantryItemId`, `data.location`; followed by the pantry item's `food.pantry_item.added`) |
| `food.shopping_items.cleared` | `food_shopping_list` / `bought` | Cleared 3 bought items from the shopping list |
| `food.store.created` / `.updated` / `.archived` / `.restored` | `food_store` | Store “Lidl” created (also when an item's `storeName` creates it) |
| `food.aisle_rule.created` / `.deleted` | `food_aisle_rule` | Aisle rule created: “kerze” → household (also inside words) |
| `food.pantry_item.added` / `.updated` / `.used_up` / `.thrown_away` / `.restocked` / `.deleted` | `food_pantry_item` | Added “Joghurt” to the pantry (fridge, expires 2026-10-14) |
| `food.pantry_item.running_low` / `.running_low_cleared` | `food_pantry_item` | “Milch” is running low; “Milch” is no longer running low (": more came home" and `data.reason: more_at_home` when more was added or put away) |
| `food.staple.created` / `.updated` / `.deleted` | `food_staple` | “Milch” is now a staple; Staple “Milch” updated: amount, store; “Milch” is no longer a staple |
| `food.preferences.updated` | `food_preferences` / `preferences` | Cooking preferences updated: methods, dislikes |
| `food.idea.created` / `.saved` / `.dismissed` | `food_idea` | Recipe idea “Air fryer wings”: Uses the Hähnchen that expires tomorrow; Saved the idea “Air fryer wings” as a recipe (followed by its `food.recipe.created`); Dismissed the idea “Soup” |
| `food.recipe.created` / `.updated` / `.deleted` | `food_recipe` | Recipe “Pasta Bolognese” created (3 ingredients); updated: title, ingredients |
| `food.meal_plan.set` / `.cleared` | `food_meal_plan` / `<date>:<slot>` | Planned “Pasta” for dinner on Mon 12 Oct, replacing “Soup”; Cleared “Pasta” from dinner on Mon 12 Oct |
| `food.notification.missed` | `food_notification` | Expiry reminder due Sun 11 Oct 2026, 08:00 CEST was missed: Food was not running (see [Expiry reminders](#expiry-reminders)) |

`data` holds the item's name, aisle or location, state, source, amount, store,
`sourceRef`, expiry date, and for updates and merges the changed fields
(`changes`). An item a staple puts on the list is a regular
`food.shopping_item.added` whose summary says why ("Added “Milch” to the
list: used up (staple)") with `data.reason` and `data.stapleId`; it follows
the pantry item's event. See [Activity](activity.md).

## Storage and operations

- Database: `food_db` / `food_user`, password `LIFE_OS_FOOD_DB_PASSWORD` in
  `.env`; migrations in `services/food/db/migrations` (`food-migrate`). A fresh
  PostgreSQL volume provisions it; for an existing deployment see
  [Operations](../operations/deployment.md#adding-the-food-service).
- Configuration: `ACTIVITY_SERVICE_URL` (compose: `http://activity:8080`;
  empty records nothing), `LIFE_OS_TIMEZONE` (the owner's "today" for
  `expiringWithinDays` and the expiry reminder's wall clock; an unknown name
  falls back to UTC, logged), and the [expiry reminders'](#expiry-reminders)
  settings. Migration 000003 adds the reminders' state
  (`food_notification_dispatch`, `food_notification_log`); 000004 the
  recipes (`recipes`, `recipe_ingredients`), the meal plan
  (`meal_plan_entries`), and the `recipe` source; 000005 the `staples`, the
  pantry's `running_low_at` (in-stock items only), and the `staple` source
  (down keeps staple items as manual ones); 000006 the `food_preferences`
  row (the air fryer preferred), `recipes.methods` (NULL: inferred), and
  `food_ideas` (down keeps the recipes saved ideas became).
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
key), the actor gate, the Activity events, putting bought items away (places
from the request or the aisle, amounts, sources, the events, refusals of
needed, unknown, and repeated items, and the idempotent replay), recipes
(create, unique titles, validation, search by title words, tag, and
ingredient, paging, updates that replace ingredients and rename the plan's
meals, deletion leaving plain-text meals), the meal plan (one meal per slot,
replacing, unchanged slots recording nothing, ranges, refusals, clearing),
missing ingredients (the dry run, joined amounts, in the pantry, on the list,
used-up items not counting, sources, the replay, a week counting each recipe
once), staples (create, unique names, stores by name, updates and clearing,
deletion, the events), restock (running low, used up, and thrown away each
adding a staple once with its amount, store, aisle, `source: staple`, and
`sourceRef`; repeating and an item already needed leaving the list alone; an
archived store left out; another item only flagged unless `addToList`;
running low by name with and without a pantry row; `not_in_stock`; more at
home clearing the flag; the idempotent replay), preferences (replace,
order and duplicates, refusals, nothing recorded for no change), recipe
methods (inferred, set, kept across edits, inferred again on null), the
suggestions (the order, every reason and its points, missing, basics,
expiring, a dislike leaving a recipe out, the meal slot, the limit, the
preference bonus going with the preference), ideas (actors, conflicts with
ideas and recipes, validation, save with its content and the replay, web
ideas keeping methods and link, dismiss, the events), and migrations
000002, 000003, 000004, 000005, and 000006 down and up. The amount join,
the ingredient grouping, the restock reasons, and the lifecycle result's
fields also have unit tests, and `internal/domain` has unit tests for the
word matcher (compounds, plurals, Russian endings, and what must not
match), the method rules in three languages, and the ranking (coverage,
the expiring bonus, the air fryer bonus, habits capped, the recency
penalty, dislikes, the meal slot). `internal/notifications` has unit
tests (the clock setting, wall times across both daylight saving changes and
west of UTC, the window, the message's lines, grouping, importance, late
phrasing, and caps) and integration tests with an in-process Notifications
that deduplicates like the real one: the first run only starts the cursor,
one reminder a day naming today and tomorrow but not food that expired
before, days with nothing to say, late and missed reminders after downtime,
failing and refusing Notifications, a restart and a cursor set back, the
lease and two processes at once, and migration 000003 down and up.
