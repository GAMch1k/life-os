# Contributing to Life OS

Work one Notion ticket per branch. Start from the current primary branch, make small coherent commits, run the ticket's relevant verification, merge only after the primary branch is healthy, then update the ticket.

Documentation is part of the definition of done. Any change to architecture, API contracts, configuration, networking, persistence, deployment/operations, or user-visible behavior must update the relevant English Markdown page under [`docs/`](docs/README.md) in the same branch. Keep links relative and readable in GitHub and Obsidian. Generated OpenAPI output supplements, never replaces, the human explanation.

Services own their own database, migrations, and domain types. Do not access another service's tables; use APIs. Do not publish individual service ports, modify host Nginx/firewall/DNS, commit secrets, or run destructive data operations without explicit authorization.
