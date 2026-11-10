# AI processing (Codex)

Without AI, what you capture in Inbox waits there for you to file it yourself. With AI processing on, Life OS reads each capture (typed, shared, photographed, or a transcribed voice note) and does what it says: "dentist Friday 10:00" becomes a calendar event, "buy oat milk" goes on the shopping list, "paid 12.50 for lunch" becomes an expense, and "move the dentist to Monday" changes the event you already have. When it is not sure, it asks you in Inbox's **Human Review** instead of guessing.

## What it uses

Life OS runs OpenAI's **Codex** with **your ChatGPT sign-in**, inside the `processor` container.

- **You need a ChatGPT plan that includes Codex.** There is no API key and no separate bill: each capture uses a little of your plan's Codex allowance. The Status page shows how much is left and when it resets. When the allowance runs out, captures wait and are processed after the reset.
- **The model** is `gpt-5.6-luna` with medium reasoning, chosen for routine filing by the [AI runtime benchmark](../verification/ai-runtime-benchmark.md). `LIFE_OS_CODEX_MODEL` and `LIFE_OS_CODEX_REASONING_EFFORT` in `.env` change it.
- The processor also runs a few optional AI jobs on the same allowance, each with an on/off setting in `.env`: categories for bank transactions (`LIFE_OS_AI_CATEGORIES`), a monthly budget plan (`LIFE_OS_AI_BUDGET_PLANS`), and daily recipe ideas from your pantry (`LIFE_OS_IDEAS_TIME`, `off` to turn them off).

## Privacy

With AI processing on, **your captures are sent to OpenAI**: the text, attached photos, and voice transcripts, together with what the AI looks up to understand them, such as matching tasks, events, shopping items, or transactions. OpenAI's terms for your ChatGPT account apply. The AI cannot reach your database directly; it acts only through Life OS's own, limited tools. Everything else, including speech-to-text, stays on your server.

If you don't want that, leave AI processing off.

## Sign in

```sh
./setup.sh codex
```

It works on a server without a browser. The script starts Codex's sign-in inside the processor's own image; Codex prints a link and a one-time code. Open the link on any device, sign in to ChatGPT, and enter the code. If the page says that device-code sign-in is off for your account, turn it on in ChatGPT's security settings and run the command again.

The sign-in is saved in `codex-home/auth.json`, next to `setup.sh`, readable only by you. The processor reads it at every start. Treat it like a password: it is not in the backups, and anyone with it can use your Codex allowance.

Afterwards, the Status page shows Codex as signed in, and a test capture in Inbox should be filed within a minute.

## Sign in again

If the Status page says Codex is signed out, run `./setup.sh codex` again.

## Turn it off

Delete the sign-in and restart the processor:

```sh
rm codex-home/auth.json
docker compose restart processor
```

Captures then wait in Inbox for you. The processor keeps restarting without a sign-in; the Status page says so, and that is expected in this preview.

## More

How the processor works, its settings, and its limits: [MCP and AI](../services/mcp.md).
