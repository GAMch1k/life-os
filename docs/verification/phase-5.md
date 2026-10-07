# Phase 5 verification

The Inbox processor is tested as a constrained AI-to-MCP pipeline. Its routing corpus covers Russian task capture, German calendar capture, an explicit-account finance transaction, multiple actions, ambiguity, hostile prompt-like text, failed tool execution, and a completed-attempt retry. Tests assert the MCP tool sequence, audit outcome, and resolution behavior without allowing the test model to mutate a real user database.

Run the corpus with:

```sh
cd services/mcp
go test ./internal/processor
```

The live processor uses the logged-in local Codex CLI. For an operational safe preview, run `go run ./cmd/processor --dry-run`; it produces proposed calls and does not create actions or alter Inbox state.
