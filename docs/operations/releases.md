# Releases: images on ghcr.io

A Life OS release is a Git tag. Pushing it runs `.github/workflows/release.yml`, which builds every image once, runs the full-stack tests against those images, scans them, and only then publishes them to the GitHub Container Registry as `ghcr.io/gamch1k/life-os-<name>:<version>`, signed and with build provenance and an SBOM. It finishes with a GitHub Release that carries the install bundle: the files a server needs to run that version without a checkout. Images come only from such a tag of `GAMch1k/life-os`: never from a pull request, a fork, a laptop, or the server.

## Versions

- **One version for the whole stack.** Every image of a release has the same tag, and `LIFE_OS_VERSION` in `.env` picks it.
- **Tags are `v<major>.<minor>.<patch>`**, [Semantic Versioning](https://semver.org/) with a `v`: `v0.1.0`. A pre-release adds a suffix: the early previews are `v0.1.0-preview.1`, `v0.1.0-preview.2`, and so on. A tag that is not a version fails the run before anything is built.
- **The image tag is the Git tag**, `v` included: `ghcr.io/gamch1k/life-os-tasks:v0.1.0-preview.1`, and `LIFE_OS_VERSION=v0.1.0-preview.1`.
- **Moving tags.** A `-preview.N` release also moves `preview` to itself; a stable release (no suffix) moves `latest`. Other pre-releases move neither. There is no `latest` until the first stable release.
- **Platform images.** Each version is an index of its platforms' images, each also tagged `<version>-<platform>` (`v0.1.0-preview.1-amd64`). Pull the version; the platform tags exist because the index is assembled from them.

## The images

The images are the services `compose.yaml` builds, plus the provisioning and the migrations. Services that run the same image share it: `voice-worker` runs Voice's image, `host-health-docker` Server health's, and every `<service>-migrate` the migrations image. `tools/build-install-bundle --images` lists them, as `docker compose build` names them here and as the release publishes them.

| Image | Runs as | What |
| --- | --- | --- |
| `life-os-postgres-provision` | `postgres-provision` | Creates and reconciles every service's database and login and the backup role, at every start ([infra/README.md](../../infra/README.md#service-database-provisioning)) |
| `life-os-migrate` | `finance-migrate`, `tasks-migrate`, … | golang-migrate and every service's migrations, at `/migrations/<service>` |
| `life-os-finance`, `life-os-tasks`, `life-os-food`, `life-os-inbox`, `life-os-calendar`, `life-os-activity`, `life-os-notifications` | the service of that name | The domain services |
| `life-os-calendar-feeds`, `life-os-google-calendar`, `life-os-banking`, `life-os-recipe-import` | the service of that name | The integrations |
| `life-os-dashboard` | `dashboard` | The Dashboard |
| `life-os-mcp`, `life-os-processor` | `mcp`, `processor` | The MCP server and the AI processor (Codex CLI only: never Claude Code, see below) |
| `life-os-voice`, `life-os-whisper` | `voice`, `voice-worker`, `whisper` | Voice and its whisper.cpp server |
| `life-os-monitor`, `life-os-backup` | `monitor`, `backup` | The monitor and the nightly backups |
| `life-os-host-health` | `host-health`, `host-health-docker` | Server health and its read-only Docker gateway |
| `life-os-gateway` | `gateway` | nginx with Life OS's configuration |

PostgreSQL itself, ntfy, and cloudflared run their upstream images, pinned by digest in `compose.yaml`.

### No Claude Code in the images

The published images never contain Claude Code: its licence reserves all rights, and Anthropic's [legal page](https://code.claude.com/docs/en/legal-and-compliance) asks for its Commercial Terms to preinstall it in a product. The release builds the processor with the default `LIFE_OS_AI_PROVIDERS=codex` (CI's full-stack job and the release both build from `.env.example`, `tools/ci-env`, and neither overrides it), so `life-os-processor` has Codex alone. A user who wants Claude Code runs `./setup.sh claude` on their own server: it downloads the official, unmodified package from Anthropic's npm registry and adds the binary to a local image, `lifeos-processor-claude:<version>`, `FROM` the release's processor image by digest ([AI processing → What it downloads](../install/ai-processing.md#what-it-downloads)). The build's files are `services/mcp/claude-code/` (Dockerfile, install.sh, package.json, and package-lock.json, which pins the version and sha512 integrity), the same ones a source build with `codex,claude` installs from, so a version bump is one change there; `tools/build-install-bundle` writes them into the release's `setup.sh` (no extra asset), and `--check` fails when that Dockerfile's Node.js image is not the processor's own.

### Self-contained: nothing from the repository at run time

A server runs a release from two files, `compose.yaml` and `.env`: no checkout. So `compose.yaml` bind-mounts no file of the repository; what used to come from one is built into an image, and settings come from `.env`:

| Was a bind mount of | Now |
| --- | --- |
| `infra/postgres/` into `postgres` (the initdb script, the registry) | The `postgres-provision` image, which provisions over the network after PostgreSQL is healthy, on every start: a fresh volume, and an update that adds a database |
| `services/<service>/db/migrations` into each `<service>-migrate` | The `migrate` image, `/migrations/<service>` |
| `infra/postgres/` into `backup` (the registry) | The `backup` image, `/etc/lifeos/postgres/service-databases.conf` |
| `infra/nginx/life-os.conf.template` and `server-names.envsh` into `gateway` | The `gateway` image; the template is still rendered at every start from `LIFE_OS_*` settings |
| `infra/ntfy/server.yml` into `ntfy` | `NTFY_*` settings in `compose.yaml` (the upstream image, unchanged) |

What stays outside the images, because it is the server's own:

- **Data: named volumes.** `lifeos-postgres-data`, `lifeos-inbox-media` (capture images), `lifeos-voice-audio`, `lifeos-whisper-models` (the model Whisper downloads on its first start), `lifeos-ntfy-data`.
- **Host paths from `.env`.** The backup directory (`LIFE_OS_BACKUP_DIR`, required), the Codex home (`LIFE_OS_CODEX_HOME_HOST_PATH`, required; an empty directory means no AI), the Enable Banking key (`LIFE_OS_ENABLE_BANKING_KEY_FILE`, else `/dev/null`), and, on the owner's server only, the Android releases directory (`LIFE_OS_ANDROID_RELEASES_DIR`, else `~/life-os.android`).
- **The host itself, for Server health.** `host-health` reads `/`, Docker's data root, and the containers' cgroups (read-only binds), and `host-health-docker` the Docker socket ([Server health](../services/host-health.md)).

`tools/build-install-bundle` refuses a `compose.yaml` that bind-mounts a relative path (a repository file, also as a variable's default such as `${NAME:-./path}`) in the bundle, uses `env_file`, or declares `configs` or `secrets`, so a new one fails CI.

Where the release bundle differs from the owner's server, `compose.yaml` says so next to the service, in `x-install-bundle` (Compose ignores `x-` keys), and only the generator applies it:

- **gateway** `omit-volumes: [/srv/android]`: no Android releases directory; `/android/` answers 404. Other people's phones update from GitHub Releases of `GAMch1k/life-os-app`.
- **processor** `image-variable: LIFE_OS_PROCESSOR_IMAGE`: the bundle's image is `${LIFE_OS_PROCESSOR_IMAGE:-<the release's processor image>}`, so `./setup.sh claude` can point the processor at the local image with Claude Code; `./setup.sh update` builds that again for the new version before it restarts anything, and `./setup.sh codex` empties it.
- **processor** `writable-volumes: [/run/lifeos-codex-source]` and `LIFE_OS_CODEX_AUTH_WRITE_BACK=on`: the Codex home is Life OS's own (`codex-home/`, which `setup.sh codex` signs in), so the processor writes a refreshed `auth.json` back to it within a minute. Codex refreshes its sign-in in the container's copy, and a refresh may use up the refresh token the mounted copy holds; without the write-back, the next restart could start signed out. It writes only `auth.json` (never Codex's sessions or history), atomically, only a file that still holds a refresh token, and never over a newer sign-in (`./setup.sh codex` wins until the next restart reads it). On the owner's server the Codex home is the owner's own, shared with their Codex CLI, and stays read-only: two independent refreshers writing one file could undo each other. Its `volume-sources` makes `LIFE_OS_CODEX_HOME_HOST_PATH` required again: on the owner's server, unset, it mounts the repository's empty `services/mcp/codex-home-unset` (for `LIFE_OS_AI_PROVIDER=claude`), which the bundle has not got; `setup.sh` always writes it.

No image holds a secret or a setting of the owner's. `.dockerignore` keeps `.env` out of every build, the Codex sign-in is mounted at run time only (Claude Code's token or API key comes from `.env` as the container starts), and a value such as a hostname or address comes from `.env` when the container starts.

## The install bundle

Every GitHub Release has these files:

| File | What |
| --- | --- |
| `compose.yaml` | `deploy/compose.yaml`: the repository's `compose.yaml` with every `build:` replaced by `image: ${LIFE_OS_IMAGE_REGISTRY:-ghcr.io/gamch1k}/life-os-<name>:${LIFE_OS_VERSION}` |
| `.env.example` | The repository's, with `LIFE_OS_VERSION` set to the release |
| `setup.sh` | Writes `.env` from `.env.example` with generated passwords and keys ([install docs](../install/)); its `release_version` placeholder is set to the release, and its `claude_build_files` carries `services/mcp/claude-code/` (`tools/build-install-bundle --setup-script` prints it) |
| `SHA256SUMS` | The SHA-256 of the three |

`deploy/compose.yaml` is generated: change `compose.yaml` and run `tools/build-install-bundle`, then commit both. `tools/check --install-bundle` (CI's `check-generated` job) fails when it is stale. Do not run it on this host: like `compose.yaml` it names the project `lifeos`, so Compose would act on the live stack.

## Cutting a release

From a clean, pushed `master` whose CI is green:

```sh
git tag v0.1.0-preview.1
git push origin v0.1.0-preview.1
```

Then watch Actions → Release. It takes about 35 minutes; its build, test, and scan job alone took 23.5 minutes in the dry run of 2026-11-10:

1. **Plan** checks the tag, that `deploy/compose.yaml` is current, lists the images, and assembles the bundle.
2. **Build, test, and scan**, once per platform on a runner of that platform: CI's full-stack tests (`.github/actions/full-stack`, the same steps as CI's `full-stack` job: `docker compose build`, the smokes, the browser suite), then Trivy on every image ([the vulnerability gate](#the-vulnerability-gate)) and each image's SBOM. The tested images are saved for the next job, which has the write access this one has not.
3. **Publish and sign** loads exactly those images (it checks their IDs), pushes them, assembles each version's index and moving tag, signs every image with cosign, and attests the images' and the bundle's build provenance.
4. **SBOM**, one job per image and platform, attests its SBOM.
5. **Release** creates the GitHub Release with the bundle and notes that start with the early-preview warning.

A failure before step 3 publishes nothing: fix it on `master`, delete the tag (`git push origin :refs/tags/v0.1.0-preview.1`, `git tag -d v0.1.0-preview.1`), and tag again. After step 3 the images exist; a re-run of the failed jobs (Actions → the run → Re-run failed jobs) finishes the release with the same images.

### Dry runs

Every run that is not a tag push is a dry run: steps 1 and 2 with the version `v0.0.0-dry-run.<run number>`, and nothing published. It runs by hand (Actions → Release → Run workflow, on any branch) and on a push to a branch that changes the release's own files (the workflow, `.github/actions/full-stack`, the allowlist, `tools/build-install-bundle`, `tools/scan-images`). A dry run may go without `setup.sh`; a release may not.

### Package visibility

GitHub's documentation disagrees with itself here. A new package of a personal account is [private by default](https://docs.github.com/en/packages/learn-github-packages/configuring-a-packages-access-control-and-visibility), while one a workflow creates with `GITHUB_TOKEN` ["inherits the visibility and permissions model of the repository"](https://docs.github.com/en/packages/managing-github-packages-using-github-actions-workflows/publishing-and-installing-a-package-with-github-actions#default-permissions-and-access-settings-for-packages-modified-through-workflows), which is public; there are reports of such packages created private anyway. No API changes a package's visibility. So the release checks: every image must answer an anonymous pull. If one does not, the release is created as a **draft**, and the run's summary links each private package. For each: Package settings → Danger Zone → Change visibility → Public, typing the package's name (it cannot be made private again). Then publish the draft release (Releases → the draft → Edit → Publish release). A package keeps its visibility, so this happens at most once per image: the first release, and the first release with a new image.

The images carry `org.opencontainers.image.source`, which links each package to this repository.

## Verifying an image

Each image is signed with cosign, keyless: the signature's certificate names the release workflow and the tag that ran it, and the signature is in the public transparency log. With [cosign](https://docs.sigstore.dev/cosign/system_config/installation/) 2.4 or later:

```sh
cosign verify ghcr.io/gamch1k/life-os-tasks:v0.1.0-preview.1 \
  --certificate-identity-regexp '^https://github.com/GAMch1k/life-os/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

The build provenance (which workflow, commit, and run built it) and the SBOM are GitHub attestations. With the [GitHub CLI](https://cli.github.com/):

```sh
gh attestation verify oci://ghcr.io/gamch1k/life-os-tasks:v0.1.0-preview.1 -R GAMch1k/life-os
gh attestation verify oci://ghcr.io/gamch1k/life-os-tasks:v0.1.0-preview.1 -R GAMch1k/life-os \
  --predicate-type https://cyclonedx.org/bom
```

The bundle's files have build provenance too: `gh attestation verify compose.yaml -R GAMch1k/life-os` (and `.env.example`, `setup.sh`), besides `sha256sum --check SHA256SUMS`.

## The vulnerability gate

`tools/scan-images` runs Trivy (the version pinned in `release.yml`, checked against its SHA-256) on every image the release publishes. A HIGH or CRITICAL vulnerability that has a fix fails the release; unfixed ones and lower severities are not gated (Dependabot and govulncheck cover the code). Every image is scanned, even after one failed. Locally, on images built here: `tools/scan-images lifeos-tasks lifeos-gateway`.

The fix is almost always an update: the base image's digest (Dependabot's weekly pull request, or by hand), an Alpine package, or a Go or npm module. Only when Life OS cannot reach the vulnerable code does it go into the allowlist, `.github/trivy-allowlist.yaml` (Trivy's ignore-file format). Each entry needs:

- `id`: the vulnerability;
- `paths` or `purls`: where it is accepted, as narrowly as possible (one binary in one image, not every image);
- `statement`: why Life OS cannot reach it, specific enough to be checked;
- `expired_at`: a date at most three months away, after which Trivy reports it again and the entry is looked at anew.

`tools/scan-images` refuses an entry without all four. An entry is never a way to ship a reachable vulnerability on time; that release waits.

## Adding a platform

`RELEASE_PLATFORMS` in `release.yml` lists the platforms; `'["amd64", "arm64"]'` adds ARM. Each platform is built and fully tested on a runner of its own (`ubuntu-24.04-arm` for arm64), scanned, and pushed, and the version's index lists every platform. Every Dockerfile must then build on that platform, and Trivy's checksum for it is already in the workflow.
