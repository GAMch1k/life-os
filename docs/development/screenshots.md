# README screenshots

The screenshots in the [README](../../README.md) show a made-up person's week in Life OS, never anyone's real data. They are made again with one script, on a throwaway stack, from a fictional demo dataset.

## The demo data

[`apps/dashboard/e2e-screenshots/demo-data.ts`](../../apps/dashboard/e2e-screenshots/demo-data.ts) holds it: tasks, a week of events, Inbox captures (three the AI filed, one waiting in Human Review, one pending), manual accounts with this month's transactions, a budget and two subscriptions, and the shopping list, pantry, recipes, and meal plan. Dates are relative to the day the screenshots are taken, so the pages look current.

Everything in it is invented: the names, places, shops, companies, and amounts. The accounts are kept by hand, so no IBAN or bank shows. Keep it that way: no real person, address, company, account number, or domain goes in there, since the images are public.

## How they are made

[`tools/screenshots`](../../tools/screenshots) does it all, on the disposable verification stack (`tools/verify-compose`, project `lifeos-verify`), never on a Life OS with real data:

1. It starts the stack as a new install runs it: the core alone, with legacy access off.
2. It takes a setup code from the Dashboard's container, as `./setup.sh setup-code` does.
3. Playwright ([`playwright.screenshots.config.ts`](../../apps/dashboard/playwright.screenshots.config.ts)) makes the first account with it, writes the demo data with that account's session, and saves each page as a PNG at twice its size: Home, Tasks, Calendar, Inbox, Finance, and Food at 1280×800, and Home and Food at a phone's 390×844. Last, it makes the repository's social preview (1280×640) from the Home screenshot.
4. [`optimize.mjs`](../../apps/dashboard/e2e-screenshots/optimize.mjs) writes the smaller WebP files the README shows next to the PNG files (1600 pixels wide, 585 for the phone), and compresses the social preview.
5. It removes the stack.

## Make them again

On GitHub: run the **Screenshots** workflow (Actions → Screenshots → Run workflow). It also runs on a pull request that changes the screenshot tooling. It needs no secret, and uploads the images as the `screenshots` artifact. Download it with:

```sh
gh run download <run-id> --name screenshots --dir /tmp/screenshots
```

Locally, on a machine for testing Life OS (it builds images and starts a stack of its own, so not on a server that runs one you use): build the core's images (`COMPOSE_PROFILES= docker compose build --pull`), install the Dashboard's dependencies and Playwright's Chromium (`npm ci --prefix apps/dashboard`, then `npx playwright install chromium` in `apps/dashboard`), and run:

```sh
tools/screenshots /tmp/screenshots
```

## Before they go into the README

Open every image and look for anything that is not made up: a real name or email address, a real IBAN or account, a real domain or server address, a real place. Then copy the WebP files and `social-preview.png` into [`docs/images/`](../images/), and keep the WebP files small: about 1.5 MB for all of them together. GitHub does not read the social preview from the repository: upload `docs/images/social-preview.png` in the repository's settings (Settings → General → Social preview).
