# Updating

Each Life OS release has a version, such as `v0.1.0-preview.2`, and one set of images for the whole stack. Your `.env` names the version you run (`LIFE_OS_VERSION`), so nothing changes until you update.

## Before you update

- **Read the release notes** on the [releases page](https://github.com/GAMch1k/life-os/releases). Previews may change how things work, and the notes say what to do.
- **Have a copy of `.env`** somewhere safe.
- The update backs up first. Make sure the Status page's Backups check is green.

## Update

Download the new release's files over the old ones, check them, and run the update:

```sh
cd ~/life-os
version=v0.1.0-preview.6
release=https://github.com/GAMch1k/life-os/releases/download/$version
for file in compose.yaml setup.sh SHA256SUMS; do curl -fsSLO "$release/$file"; done
# GitHub serves .env.example as default.env.example
curl -fsSL -o .env.example "$release/default.env.example"
sha256sum -c SHA256SUMS
./setup.sh update
```

`./setup.sh update`:

1. backs up every database, and stops if that fails (`--skip-backup` updates without a backup);
2. adds the settings the new `.env.example` has and your `.env` lacks, with new passwords where needed, and lists them. Coming from a release before [modules](modules.md) (up to `v0.1.0-preview.5`, when every module ran), it first turns on the modules your install used and says which ([Updating an install from before modules](modules.md#updating-an-install-from-before-modules));
3. downloads the new version's images (the core's, and those of the modules that are on), and stops without changing anything if that fails;
4. with AI processing on Claude Code, adds Claude Code to the new version's processor image on your server ([What it downloads](ai-processing.md#what-it-downloads)), and stops without changing anything if that fails;
5. writes the new version into `.env`, removes the containers of modules that are off, restarts what changed, and waits until Life OS answers. On the way up, `postgres-provision` creates any database the new version adds (with the password step 2 added), and each service's migrations bring its database up to date.

Your `.env`, your data, and your backups stay as they are.

## Pin a version

`LIFE_OS_VERSION` in `.env` is the exact version you run; `./setup.sh` writes the version of the release you downloaded. `./setup.sh update <version>` moves to another one. The moving tag `preview` always means the newest preview: avoid it, since an update then happens whenever the images are downloaded again.

## Going back

Going back to an older version is not supported: an update can change the databases, and those changes only go forward. If an update goes wrong, restore the backup it took first ([Backups](backups.md#restore)), with the old release's files.

## Verify the images

Every image is built from a release tag by the project's GitHub Actions, scanned for vulnerabilities, and signed, with build provenance (a GitHub attestation) and a list of what it contains (SBOM). With the [GitHub CLI](https://cli.github.com/), you can check that an image came from this repository:

```sh
version=$(sed -n 's/^LIFE_OS_VERSION=//p' .env)
gh attestation verify "oci://ghcr.io/gamch1k/life-os-tasks:$version" -R GAMch1k/life-os
```

[Releases](../operations/releases.md) describes how releases are made, and [how to check an image](../operations/releases.md#verifying-an-image) with cosign too.
