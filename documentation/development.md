# Development workflow

Work from a ticket: read its dependencies and acceptance criteria, create one branch, make small coherent commits, verify, update documentation, merge into the actual primary branch, and then mark the ticket done. Do not combine unrelated tickets in a feature branch.

To add a service, run `make create-service SERVICE=<kebab-name>`, then give it an owned database, migrations, OpenAPI surface, Compose service, gateway route if browser-accessible, dashboard client/types where needed, and tests. A new endpoint begins in OpenAPI, followed by regeneration, implementation, tests, gateway exposure if appropriate, and documentation.

Useful commands:

```sh
make life-up          # build/start all services behind gateway
make life-smoke       # browser-facing smoke checks
make e2e-smoke        # isolated clean-stack end-to-end verification
make verify-clean-db  # disposable database isolation/migration validation
make check            # contract, codegen, Go, dashboard quality gate
```

No domain service may use another domain's tables. Do not modify host networking or Nginx from a repository ticket.
