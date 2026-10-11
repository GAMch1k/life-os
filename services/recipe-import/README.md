# Recipe import service

The integration that reads recipes from links and pasted text and saves them
in Food. It fetches pages itself (http or https, public addresses only, 10 s,
5 MB, no cookies), reads a schema.org `Recipe` from JSON-LD or microdata, falls
back to an OpenGraph preview tagged `to-review` (Instagram, TikTok, YouTube,
blocked pages), reads pasted captions with simple rules, and saves recipes
through Food's `POST /v1/recipes` as `system:recipe-import`. It also
discovers recipes by main ingredient in TheMealDB (`GET /v1/discover`,
cached, drafts only). It has no database.

Gateway base: `/api/integrations/recipe-import/`. Contract:
`api/openapi.yaml`. See [docs/services/recipe-import.md](../../docs/services/recipe-import.md).

```text
api/openapi.yaml        # the contract
cmd/server/             # process entrypoint
internal/api/           # HTTP adapters
internal/config/        # configuration
internal/fetch/         # the safe page download (the address guard)
internal/food/          # Food's API client
internal/recipe/        # the parsers (pure; fixture pages in testdata/)
```

`make test` runs every test offline: the parsers against saved pages, the
address guard against local servers, and the API against a Food stand-in.
