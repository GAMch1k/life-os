# AI processing (Codex)

AI processing is the `ai` [module](modules.md), off on a new install. Without AI, what you capture in Inbox stays there as you wrote it, for you to file yourself. With AI processing on, Life OS reads each capture (typed, shared, photographed, or a transcribed voice note) and does what it says: "dentist Friday 10:00" becomes a calendar event, "buy oat milk" goes on the shopping list, "paid 12.50 for lunch" becomes an expense, and "move the dentist to Monday" changes the event you already have. When it is not sure, it asks you in Inbox's **Human Review** instead of guessing.

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
./setup.sh up
```

Signing in turns the `ai` module on; `./setup.sh up` starts the processor and MCP.

It works on a server without a browser. The script starts Codex's sign-in inside the processor's own image; Codex prints a link and a one-time code. Open the link on any device, sign in to ChatGPT, and enter the code. If the page says that device-code sign-in is off for your account, turn it on in ChatGPT's security settings and run the command again.

The sign-in is saved in `codex-home/auth.json`, next to `setup.sh`, readable only by you. The processor reads it at every start, and when Codex refreshes the sign-in, the processor writes the new one back there within a minute, so a restart keeps you signed in. Treat it like a password: it is not in the backups, and anyone with it can use your Codex allowance.

Afterwards, the Status page shows Codex as signed in, and a test capture in Inbox should be filed within a minute.

## Sign in again

If the Status page says Codex is signed out, run `./setup.sh codex` again.

## Turn it off

```sh
./setup.sh modules off ai
```

It removes the processor and MCP containers; captures then stay in Inbox as you wrote them, and the Status page leaves AI processing out. The sign-in stays in `codex-home/`, so `./setup.sh modules on ai` and `./setup.sh up` turn it on again. To delete the sign-in too, `rm codex-home/auth.json`.

## Claude Code instead of Codex

Instead of Codex, the processor can run Anthropic's **Claude Code**, signed in with **your Claude plan** (Pro or Max) or **your own Anthropic API key**. With it, **your captures are sent to Anthropic** instead of OpenAI, as described under Privacy above. Claude Code is Anthropic's software, not part of Life OS: your use of it is under [Anthropic's terms](https://code.claude.com/docs/en/legal-and-compliance) for your account. Life OS is not made or endorsed by Anthropic.

```sh
./setup.sh claude
./setup.sh up
```

### What it downloads

Life OS's published images do not contain Claude Code. `./setup.sh claude` downloads it onto your server and adds it to a local copy of the processor image:

- **From where:** Anthropic's official npm package, [`@anthropic-ai/claude-code`](https://www.npmjs.com/package/@anthropic-ai/claude-code), from the npm registry. `npm ci` installs exactly the version this release pins and checks each download against the sha512 the release records (`services/mcp/claude-code/package-lock.json`); the package's own installer then picks the native build for your server (Linux, musl).
- **What:** only Claude Code's native binary goes into the image, unmodified, as `/usr/local/bin/claude`. It is about 330 MB (311 MiB) unpacked on amd64, so the local image is that much bigger than the published one. npm itself does not stay in the image.
- **Where it goes:** a local image, `lifeos-processor-claude:<version>`, built `FROM` the release's own processor image of your version (by its digest), with Codex still in it. `.env` gets `LIFE_OS_PROCESSOR_IMAGE` pointing at it, and the release's `compose.yaml` runs that image for the processor instead of the published one.
- It needs the internet for the npm registry (and the release image, if this server does not have it yet), and about 0.5 GB of free disk while it builds.

### Sign in: plan or API key

The script asks how Claude Code should sign in (one of the two, never both):

1. **Your Claude plan.** On any computer with Claude Code, run `claude setup-token`, sign in with your plan, and paste the long-lived token it prints (`LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN`). Each capture uses a little of your plan's usage limits; there is no separate bill. Life OS then never passes an Anthropic API key to Claude Code.
2. **An Anthropic API key** (`sk-ant-api…`, from the Claude Console; `LIFE_OS_ANTHROPIC_API_KEY`). API use is billed per use by Anthropic, to your Console account.

Input is hidden, and the script never prints it. Then it asks for the model: `sonnet` (the default; Enter keeps the current one), `opus` (the most capable; uses your plan's limits or API credit faster), `haiku` (the fastest), or a full model name such as `claude-sonnet-4-5` (`LIFE_OS_CLAUDE_MODEL`). `LIFE_OS_CLAUDE_EFFORT` in `.env` sets the effort.

Without questions (`--yes`), set `LIFE_OS_CLAUDE_CODE_OAUTH_TOKEN` or `LIFE_OS_ANTHROPIC_API_KEY` (and optionally `LIFE_OS_CLAUDE_MODEL`) in the environment. The token and the key are as sensitive as a password: they are in `.env` only, not in the backups.

### Updates

`./setup.sh update` adds Claude Code to the new version's processor image too, before it restarts anything; until it has, the processor keeps the old version. `./setup.sh up` refuses to start a local image built for another version than `LIFE_OS_VERSION`, and builds a missing one again. `./setup.sh status` shows the sign-in kind (never the value), the model, and whether the local image matches your version. A newer Claude Code comes with a newer Life OS release; the processor never updates Claude Code by itself.

If the Status page says Claude Code is signed out, create a new token (or API key) and run `./setup.sh claude` again. To go back to Codex, run `./setup.sh codex`, then `./setup.sh up`: the processor runs the release image again, and the local Claude Code image is removed.

## More

How the processor works, its settings, and its limits: [MCP and AI](../services/mcp.md).
