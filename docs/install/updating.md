# Updating

Each Life OS release has a version, such as `v0.1.0-preview.2`, and one set of images for the whole stack. Your `.env` names the version you run (`LIFE_OS_VERSION`), so nothing changes until you update.

## Before you update

- **Read the release notes** on the [releases page](https://github.com/GAMch1k/life-os/releases). Previews may change how things work, and the notes say what to do.
- **Have a copy of `.env`** somewhere safe.
- The update backs up first. Make sure the Status page's Backups check is green.

## Update

A release's images need that release's `compose.yaml` and `.env.example`: a newer release adds services (such as the accounts' database) and settings the older files lack. So the new release's files come first, and the update runs with its `setup.sh`. Download them over the old ones, check them, and run the update:

```sh
cd ~/life-os
version=v0.1.0-preview.7   # the release to update to, from the releases page
release=https://github.com/GAMch1k/life-os/releases/download/$version
for file in compose.yaml setup.sh SHA256SUMS; do curl -fsSLO "$release/$file"; done
# GitHub serves .env.example as default.env.example
curl -fsSL -o .env.example "$release/default.env.example"
sha256sum -c SHA256SUMS
./setup.sh update
```

Never run `./setup.sh update <newer version>` with the old files instead: an old `setup.sh` would start the new images with the old `compose.yaml`. From `v0.1.0-preview.7` on, `setup.sh` refuses a version other than its own release's and prints the commands above; `v0.1.0-preview.6` and older do not refuse it, so updating from them needs [the files first](#updating-from-v010-preview6). `setup.sh` also refuses files of two releases mixed together: from `v0.1.0-preview.7` on, `compose.yaml` and `.env.example` name their release, and a release's `setup.sh` needs both to name its own, so a `compose.yaml` kept from an older release (which names none) is refused before anything changes. The first setup refuses them, and so does every command that starts containers from these files: `up`, `update`, `rotate-keys`, `codex`, and `ntfy`.

`./setup.sh update`:

1. backs up every database, and stops if that fails (`--skip-backup` updates without a backup);
2. adds the settings the new `.env.example` has and your `.env` lacks, with new passwords and keys where needed, and lists them. Coming from a release before [accounts](accounts.md), that includes the accounts' secret and keys, and `LIFE_OS_LEGACY_ACCESS=on`, so that Life OS keeps working without a sign-in, as before, until you create your account and turn [legacy access](accounts.md#legacy-access) off. Coming from a release before [modules](modules.md) (up to `v0.1.0-preview.5`, when every module ran), it first turns on the modules your install used and says which ([Updating an install from before modules](modules.md#updating-an-install-from-before-modules));
3. downloads the new version's images (the core's, and those of the modules that are on), and stops without changing anything if that fails;
4. with AI processing on Claude Code, adds Claude Code to the new version's processor image on your server ([What it downloads](ai-processing.md#what-it-downloads)), and stops without changing anything if that fails;
5. writes the new version into `.env`, removes the containers of modules that are off, restarts what changed, and waits until Life OS answers. On the way up, `postgres-provision` creates any database the new version adds (with the password step 2 added), and each service's migrations bring its database up to date;
6. while Life OS has no account yet, prints a one-time setup code for the first one ([Accounts](accounts.md#the-first-account)).

Your `.env`, your data, and your backups stay as they are.

## Updating from v0.1.0-preview.6

`v0.1.0-preview.7` brings [accounts and a login](accounts.md). Update from `v0.1.0-preview.6` (or older) this way, and only this way:

1. **Download the new release's files first**, over the old ones, and check them: the commands under [Update](#update), with `version=v0.1.0-preview.7` (or newer).
2. **Then run the new script:** `./setup.sh update`, without a version. Do not run `./setup.sh update v0.1.0-preview.7` with preview.6's files: preview.6's script would pull the new images with its own `compose.yaml`, which has no accounts' database and none of their settings, and the Dashboard would not start. If that already happened, download the new files and run `./setup.sh update` again, which adds what was missing (with `--skip-backup` if it cannot back up first because a service is down; take a backup as soon as Life OS runs again).

What changes:

- **Legacy access is on.** The update writes `LIFE_OS_LEGACY_ACCESS=on` into `.env`, so Life OS keeps working as before: whoever reaches it acts as the owner, without signing in, and your browsers and phone keep working. The update says so when it ends.
- **A setup code for your account.** While no account exists, `./setup.sh update` (and `up`) prints a one-time setup code, and `./setup.sh setup-code` prints a new one (valid 24 hours). Open Life OS, go to **/setup**, and create the owner's account with it ([The first account](accounts.md#the-first-account)); then turn on two-factor sign-in in **Settings → Security**.
- **Then turn legacy access off**: pair the phone app (0.5.0 or newer) with your account ([Phone and watch apps](phone-apps.md)), and turn legacy access off in **Settings → Security** or with `./setup.sh legacy-access off`. From then on, everything needs a sign-in.
- **Signing in where you open Life OS.** Signing in works at an `https://` address and at `http://127.0.0.1` or `http://localhost` (an SSH tunnel). At a plain-HTTP LAN or VPN address it is off until you allow it there: `./setup.sh gateway --allow-http-login`, then `./setup.sh up` (your password then crosses that network unencrypted; HTTPS is better). See [Accounts → Signing in](accounts.md#signing-in).
- **Helpers and scripts.** `./setup.sh feed` works without legacy access; your own scripts that call the API need an [API token](accounts.md#api-tokens) once legacy access is off.

## Pin a version

`LIFE_OS_VERSION` in `.env` is the exact version you run; `./setup.sh` writes the version of the release you downloaded, and `./setup.sh update` the version of the files it came with. To move to another version, download that release's files ([Update](#update)). The moving tag `preview` always means the newest preview: avoid it, since an update then happens whenever the images are downloaded again.

## Going back

Going back to an older version is not supported: an update can change the databases, and those changes only go forward. If an update goes wrong, restore the backup it took first, with the old release's files: download them as for an update, then run `./setup.sh update --skip-backup`, `./setup.sh restore <the backup>` (`./setup.sh restore list` lists them), and `./setup.sh up` ([Backups → Restore](backups.md#restore)).

## Verify the images

Every image is built from a release tag by the project's GitHub Actions, scanned for vulnerabilities, and signed, with build provenance (a GitHub attestation) and a list of what it contains (SBOM). With the [GitHub CLI](https://cli.github.com/), you can check that an image came from this repository:

```sh
version=$(sed -n 's/^LIFE_OS_VERSION=//p' .env)
gh attestation verify "oci://ghcr.io/gamch1k/life-os-tasks:$version" -R GAMch1k/life-os
```

[Releases](../operations/releases.md) describes how releases are made, and [how to check an image](../operations/releases.md#verifying-an-image) with cosign too.
