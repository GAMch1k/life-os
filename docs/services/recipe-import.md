# Recipe import service

`recipe-import` is the integration that saves recipes from links and pasted text in [Food](food.md) (P15-07). It is a separate, replaceable service: it fetches and reads pages itself and hands recipes to Food only through Food's public API (`POST /v1/recipes`), as `system:recipe-import`. Food stores the recipes; recipe-import keeps nothing, so it has no database.

- Code: `services/recipe-import`. No database.
- Contract: `services/recipe-import/api/openapi.yaml`. Gateway base: `/api/integrations/recipe-import`, and the Dashboard's proxy route of the same path. `/health` and `/ready` are available.
- Actor towards Food: `system:recipe-import`.

## Who owns what

| recipe-import | Food |
| --- | --- |
| Fetching pages safely, reading them (JSON-LD, microdata, OpenGraph), reading pasted captions | The recipes: title (unique), ingredients, steps, tags, source link, note |
| Activity: one `food.recipe.imported` per saved import, by whoever asked | Activity: `food.recipe.created` for the recipe, by `system:recipe-import`, caused by the import |

## API

Every request is a POST and needs `X-Life-OS-Actor`.

| Request | Behavior |
| --- | --- |
| `POST /v1/imports/preview` | `{url}`: fetches the page and returns `{method, reason, draft}`. Nothing is saved. A page that cannot be fetched or read is still 200, with `method: failed` and the reason; a link that is not http(s), or a literal private address, is 400 `invalid_request`. |
| `POST /v1/imports/text` | `{text, sourceUrl?}`: reads pasted text, such as a caption, and returns a draft with `method: text`. Nothing is saved. |
| `POST /v1/imports` | `{url, overrides?}`: saves the recipe in Food with the link as its `sourceUrl` and returns `{method, recipe}` (201; `recipe` is Food's). With `overrides` (the draft as the owner edited it) that draft is saved and the page is not fetched again (`method: edited`); without, the page is fetched and read as for a preview and what it gives is saved, a preview stub too (422 `import_failed` when it gives nothing). A title Food already has is 409 `recipe_exists` with that recipe's `recipeId`. The `Idempotency-Key` header is passed on to Food (prefixed `recipe-import:`), so a retried save creates the recipe once. Food unreachable or failing is 502 `food_unavailable`; a recipe Food refuses is 400 with Food's message. |

A **draft** has `title` (empty when none was found), `servings`, `ingredients` (`{name, amount?}`), `steps` (one per item), `tags`, `sourceUrl`, `imageUrl`, `description`, and `siteName`. Saving puts the steps one per line, leaves out empty ingredients and tags Food would refuse (over 40 characters, or without a letter or a digit), clips to Food's limits, and puts the description and the image link (`Image: <url>`) in the recipe's note, since Food has no image field.

## Reading a page

The `method` says how the draft was obtained:

- **`structured`**: the page has a schema.org `Recipe`. JSON-LD is read first: every `<script type="application/ld+json">` block, an object or an array of them, through `@graph` and `mainEntity`; invalid blocks and other types are passed over. `name` is the title; `recipeYield` (4, "4", "4 Portionen", "Serves 4", or a list) the servings when it holds a number from 1 to 100; `recipeIngredient` the ingredients, lines ending in ":" ("For the dressing:") left out as group headings; `recipeInstructions` the steps, whether text (split at lines and at block-level tags), a list of texts, `HowToStep`s, or `HowToSection`s holding steps (the sections' names are left out); `recipeCategory`, `recipeCuisine`, and `keywords` the tags (lower case, at most 10); `image` (a link, an `ImageObject`, or a list) the image, else `og:image`; `description`, else the page's. Without JSON-LD, schema.org microdata (`itemtype` …/Recipe, with `itemprop` names) is read the same way, a nested item's (an author's) names left out.
- **`preview`**: no recipe data, but a title: the stub has `og:title` (else `twitter:title` or `<title>`), `og:description` (else the meta description), `og:image`, and the tag `to-review`. Instagram, TikTok, YouTube, Facebook, Threads, X, and Pinterest posts end up here (or as `failed`), with the reason "Instagram posts can't be read directly: paste the caption to fill in the ingredients and steps."; an Instagram preview's description is the caption without the "1,234 likes, 56 comments - … on …:" prefix.
- **`failed`**: nothing to read (no recipe and no title), or the page could not be fetched; the reason says why in plain words ("The page answered HTTP 404.", "The site would not show the page to Life OS (HTTP 403).", "The site did not answer in time."). A social post adds the caption hint, and the draft still has the link and `to-review`, so the caption can be pasted.

**Ingredient lines** are split cheaply: a leading amount (a number, a range such as "2-3", a fraction such as "1/2", "½", or "1 ½") with an optional unit (g, kg, ml, l, EL, TL, Prise, Pck., Dose, Bund, Zehe, Stück, tbsp, tsp, cup, clove, can, ст. л., ч. л., шт, г, кг, мл, стакан, зубчик, and more) followed by the name: "200 g Mehl" is `200 g` of `Mehl`, "2 große Zwiebeln" is `2` of `große Zwiebeln`, "1 ст. л. сахара" is `1 ст. л.` of `сахара`. Anything else ("Salz und Pfeffer", "500g/1lb 2oz lamb mince") is the whole line as the name.

**Pasted text** (`/v1/imports/text`) is read by simple, deterministic rules: "Ingredients", "Zutaten", "Ингредиенты" (and "You need", "Du brauchst", "Нам понадобится" …) start the ingredients, also with words after them ("Zutaten für 4 Personen"); "Steps", "Method", "Instructions", "Zubereitung", "Anleitung", "Приготовление" … start the steps. Bullets ("-", "•", "*", emoji) and numbers ("1.", "2)", "Step 1:", "Schritt 1", "Шаг 1") are stripped. Without headings, a line that starts with an amount, or a short bulleted line, is an ingredient, and numbered lines and lines after the ingredients are steps. The first other short line is the title (without trailing emoji), hashtags become tags, and "für 4 Personen", "serves 4", "4 servings", or "на 4 порции" the servings. A text with neither ingredients nor steps says so in `reason`. The AI may add a smarter path later through the processor.

## Safe fetching

Pages are fetched over http or https only, and only from public addresses: after DNS resolution, every connection to a loopback, private (including the Docker networks), link-local (including cloud metadata at 169.254.169.254), unique-local, or other special-purpose address is refused, a literal internal address is refused before anything is fetched, and every redirect (at most 5) is checked again. The guard is calendar-feeds'. A download is capped at `RECIPE_IMPORT_MAX_BYTES` (5 MiB) and `RECIPE_IMPORT_TIMEOUT_SECONDS` (10), never goes through an environment proxy, sends no cookies (no cookie jar), and sends a browser-like User-Agent ending in `LifeOS-RecipeImport/1.0`, since many recipe sites answer a bare client with a blocked page. Only HTML is read; other charsets (windows-1252) become UTF-8.

`RECIPE_IMPORT_ALLOW_HOSTS` names hosts fetched whatever their address: the verification stack's overlay (`tools/fixtures/calendar-ics.compose.yaml`) sets it to its fixture server, `ics-fixture`, which serves the pages the browser tests write. Never set it on the deployed stack.

## Activity

A saved import records `food.recipe.imported` (source `recipe-import`) with the request's actor (the owner, or the AI), its correlation, subject `food_recipe` and the recipe's id, a summary such as "Imported the recipe “Spaghetti Bolognese” from chefkoch.de", and `title`, `method`, `site`, `sourceUrl`, and the ingredient count. Its id is known before the save and sent to Food as the causation, so Food's `food.recipe.created` (by `system:recipe-import`) is caused by it in the Event Log; with an Idempotency-Key the id is derived from the link and the key, so a retried save records it once. Previews record nothing.

## For the AI

The AI capture path (a later ticket) calls this service, not Food, for links: `POST /v1/imports/preview` to read a link and `POST /v1/imports` (with an Idempotency-Key derived from the capture) to save it, or `POST /v1/imports/text` for a pasted caption. A link is worth importing when it is http(s) and the capture is about a recipe or food, or it points to a known recipe site or a social post; the preview's `method` then says what it is: `structured` is a recipe, `preview` a page without recipe data (a social post: save it only with what the caption gives), `failed` nothing to save. A 409 `recipe_exists` means the recipe is already saved (`recipeId`).

## Configuration

| Variable | Default | Meaning |
| --- | --- | --- |
| `FOOD_SERVICE_URL` | required | Food (compose: `http://food:8080`). |
| `ACTIVITY_SERVICE_URL` | empty | Activity (compose: `http://activity:8080`); empty records nothing. |
| `RECIPE_IMPORT_TIMEOUT_SECONDS` | `10` | How long a page may take, 1 to 60. |
| `RECIPE_IMPORT_MAX_BYTES` | `5242880` | How large a page may be, 64 KiB to 20 MiB. |
| `RECIPE_IMPORT_ALLOW_HOSTS` | empty | Hosts fetched from any address, comma-separated: the verification stack's fixture server only. |

It needs outbound http(s) from the `backend` network to fetch pages.

## Tests

`make test` runs offline: `internal/recipe` reads saved pages in `testdata/` (a Chefkoch-like page with JSON-LD in `@graph`, a BBC-like page with `HowToSection`s and a relative image, a blog page with a recipe among invalid blocks, other types, and noise, a microdata page, an Instagram-like page with only OpenGraph, and a page with nothing), splits ingredient lines (DE, EN, RU, fractions, ranges), reads captions (headings, bullets, numbers, hashtags, servings, in DE, EN, and RU), and applies the limits; `internal/fetch` tests the address guard on real local connections (literal and resolved loopback, redirects to internal addresses, host names, cloud metadata, and other schemes, a redirect loop), the limits, refusals, charsets, the headers sent, and the test allowlist; `internal/api` runs the API against a local page server and stand-ins for Food and Activity (previews, text, saving as the integration with the key, correlation, and causation passed on, edited drafts saved without fetching, `recipe_exists` with the id, Food refusing or failing, and the import's Activity event).
