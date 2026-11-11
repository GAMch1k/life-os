# Android releases: /android/ and GitHub Releases

Life OS serves its Android apps, the phone app and the Wear OS app (built in the separate `life-os-app` repository), from the gateway at `/android/` under its public URL (`LIFE_OS_PUBLIC_URL`, such as `https://lifeos.example.com/android/`), behind Cloudflare Access like every other path. The phone installs the first version from the download page there; after that, the app reads the manifest at `/android/updates` and updates itself. Nothing here builds or signs an app: the server only stores and serves APKs that the app repository built and signed.

The same publish also makes each build a [GitHub Release](#github-releases) of [GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app/releases), with the same signed APKs and a manifest of the same form (P20-06). That is where other people's apps get their updates: an app that is not connected to the official server checks the latest GitHub release instead of `/android/`, which stays behind Access (the owner's decision, 2026-11-10).

```mermaid
flowchart LR
  Build["life-os-app build<br>(signs the APKs)"] -- "release dir:<br>release.json + APKs" --> Publish["tools/publish-android-release<br>(on the host, as the owner)"]
  Publish -- "copies, writes updates.json + index.html" --> Dir["releases directory<br>~/life-os.android (the owner's)"]
  Dir -- "read-only bind at /srv/android" --> Gateway["gateway /android/"]
  Phone["Owner's phone: browser (first install),<br>app (updates)"] -- "https://lifeos.example.com/android/…" --> Access[Cloudflare Access] --> Gateway
  Publish -- "gh: draft with the APKs + updates.json,<br>then published as the latest" --> GitHub["GitHub Release<br>GAMch1k/life-os-app"]
  Others["Other people's phones<br>(another server, or none yet)"] -- "github.com/…/releases/latest/download/updates.json" --> GitHub
```

## The flow

1. **Build** (in `life-os-app`). Its build script writes a release directory: `lifeos-phone-<versionName>-<versionCode>.apk`, `lifeos-wear-<versionName>-<versionCode>.apk`, and [`release.json`](#releasejson-what-the-build-writes).
2. **Publish** (here, on the host, as the owner): `tools/publish-android-release <release-dir> [--notes "…"]`. It checks the release, copies the APKs into the releases directory, and writes the [manifest](#the-manifest-updatesjson) and the download page. Nothing needs a restart: the gateway serves the new files at once. Then it publishes the same build as a [GitHub release](#github-releases) (`--no-github` skips that).
3. **Install.** The first time, open `https://lifeos.example.com/android/` (your public URL) on the phone (Access asks for its code if the session has expired), tap **Download**, open the file, and allow installing apps from the browser when Android asks (*Install unknown apps*). The watch APK is listed separately on the page; install it with `adb install` over the watch's wireless debugging. Anyone else downloads the phone APK from the [releases page](https://github.com/GAMch1k/life-os-app/releases) instead.
4. **Update.** The app fetches the manifest of its [update source](#where-the-app-looks-for-updates), compares `apps.phone.versionCode` with its own, downloads `url`, checks `size` and `sha256`, checks that the APK is this app at that version signed by the installed app's own certificate, and hands it to Android's package installer, which checks the signature once more ([Security model](#security-model)).

## The releases directory

`LIFE_OS_ANDROID_RELEASES_DIR` in `.env`, by default `~/life-os.android` of the user who runs Compose. It belongs to that user, the owner, mode 755; the publish script writes it as the owner and never needs root. The gateway mounts it read-only at `/srv/android` (`compose.yaml`) and only reads it (nginx runs as uid 101 in the container, hence the world-readable modes: the directory 755, the files 644).

It holds `updates.json`, `index.html`, and the APKs (`lifeos-phone-*.apk`, `lifeos-wear-*.apk`); during a publish also temporary `.publish-*` files, which the gateway never serves. Put nothing else there: the gateway would not serve it, and the publish script deletes every APK the manifest no longer lists.

### Setting it up (once)

```sh
install -d -m 755 ~/life-os.android
docker compose up -d --no-build --no-deps --force-recreate gateway
curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:3000/android/updates   # 404 until the first publish, then 200
```

The gateway must be recreated for the new mount (a reload does not add mounts) and for the new `/android/` locations in `infra/nginx/life-os.conf.template`. The `curl` uses the gateway's address and port (`LIFE_OS_GATEWAY_BIND_ADDRESS:LIFE_OS_GATEWAY_PORT`, by default `127.0.0.1:3000`). Create the directory first: if it is missing when the gateway is created, Docker creates it owned by root, so that the gateway still starts, and the publish script then refuses with the fix (`sudo chown "$(id -u):$(id -g)" ~/life-os.android`).

## Publishing

```sh
tools/publish-android-release ~/life-os-app/dist --dry-run --notes "Tasks open from notifications"   # what it would do, changing nothing
tools/publish-android-release ~/life-os-app/dist --notes "Tasks open from notifications"
```

`--dry-run` makes every check below, the GitHub ones included (read-only), and prints what a publish would do (`dry run: would publish …`, `dry run: github: would create the release …`) without changing anything here or on GitHub.

Before it changes anything, the script checks:

- `release.json`: every [field](#releasejson-what-the-build-writes) of each app it describes (the phone, the watch, or both);
- the versions: each app's `versionCode` must be greater than the published one. A lower one (a downgrade), or the same one with other bytes, is refused; `--force` publishes it anyway ([Keeping old versions](#keeping-old-versions)). The same release again is not an error: it publishes nothing;
- every APK: present under the name `lifeos-<app>-<versionName>-<versionCode>.apk`, with the size and SHA-256 `release.json` gives.

Then it copies the APKs into the releases directory, writes `updates.json` and `index.html` (each to a temporary file in the directory, renamed into place, so a reader sees the old file or the new one), deletes the APKs older than the newest 5 of each app, and prints what it did:

```text
published phone 0.1.2 (3): /android/lifeos-phone-0.1.2-3.apk, 18234567 bytes, sha256 9f2c…
published wear 0.1.2 (3): /android/lifeos-wear-0.1.2-3.apk, 7345678 bytes, sha256 41d0…
deleted lifeos-phone-0.0.7-1.apk (keeping the newest 5 of each app)
manifest: /home/you/life-os.android/updates.json (served at /android/updates), page: /home/you/life-os.android/index.html (served at /android/)
```

The APKs go in before the manifest, so the manifest never names a file that is not there yet; a publish interrupted at any point (even killed) leaves the old manifest and its APKs as they were, and the next publish removes the temporary files it left. One publish runs at a time (a lock on the directory). `--notes` goes with every app the run publishes and is shown on the page (escaped). Exit status: 0 published (or nothing new), 2 refused (nothing changed), 1 failed. The directory comes from `LIFE_OS_ANDROID_RELEASES_DIR` in the environment, else `.env`, else the default; the page shows times in `LIFE_OS_TIMEZONE` (else Europe/Berlin), and in the browser's own zone once it has loaded.

Then the [GitHub step](#github-releases) prints one line: `github: published v0.1.2-3 on GAMch1k/life-os-app as the latest release: https://github.com/…`, `github: unchanged, …` (the release is there with these APKs), or `github: skipped (--no-github)`.

## GitHub Releases

Each publish also makes the build a release of the app repository on GitHub, `LIFE_OS_ANDROID_GITHUB_REPO` (the environment, else `.env`; by default `GAMch1k/life-os-app`; `off` turns the step off, `--no-github` skips it for one run). It runs on the host with [gh](https://cli.github.com/), logged in as an account that may push to that repository (`gh auth status`). Only the signed APKs are uploaded: the signing key stays where the build ran, and no CI builds or signs a release.

The release is tagged `v<versionName>-<versionCode>`, such as `v0.4.6-13`, at the build's commit (`gitCommit` of `release.json`), titled "Life OS for Android 0.4.6 (13)", and holds three files:

| Asset | What |
| --- | --- |
| `lifeos-phone-<versionName>-<versionCode>.apk` | the phone APK, as built and signed |
| `lifeos-wear-<versionName>-<versionCode>.apk` | the watch APK |
| `updates.json` | a [manifest](#the-manifest-updatesjson) of the same form, `formatVersion` 1, with both apps; each `url` is that APK's download address in this release, `https://github.com/GAMch1k/life-os-app/releases/download/v0.4.6-13/lifeos-phone-0.4.6-13.apk`, and `previous` is empty (older versions are older releases) |

The notes give `--notes` (else the notes the server has for that version), the commit, both APKs' SHA-256, and how to install.

**Before anything changes**, here or on GitHub, the script checks, read-only, and refuses (exit 2) when one fails:

- `gh` is there, sees the repository, and may push to it;
- the release has both apps, of one build: the same `versionName`, `versionCode` and commit (`tools/build` always writes both);
- the commit is a full SHA, is on GitHub (push it first), and is not `-dirty`: a build from uncommitted changes never goes to GitHub, not even with `--force`;
- the versionCode is above the latest GitHub release's, read from its `updates.json` (`--force` allows a lower one, which then becomes the latest for new installs; installed apps never go back);
- a release with this tag that is already published must hold the same APKs (then nothing is done: `github: unchanged`); with other APKs it is refused: build with a higher `versionCode`.

**How it publishes**: `gh release create --draft` with the three files; then it reads back what GitHub stored (each asset's name, size, and SHA-256 digest) and, only if all three match, publishes the draft and marks it the latest release. Until then nobody sees it, so an app never finds a release without its files. A draft that an interrupted publish left is deleted and made again.

**When the GitHub step fails** after the server's part (no network, GitHub's error), the script says so and exits 1: everything at `/android/` is published, the GitHub release is not. Run the same command again: the server's part publishes nothing the second time, and the GitHub step finishes the release.

### Where the app looks for updates

The phone app and the watch app (through the phone) decide by the server set in the app (`life-os-app`'s `UpdateSource`):

| The app is connected to | Manifest | APKs | Token |
| --- | --- | --- | --- |
| the update origin, the official server (`lifeos.updatesUrl`, `https://lifeos.gamch1k.com`): the owner | `/android/updates` there, through Access | paths under `/android/` on that origin | the server's service token |
| any other server, or none yet | `https://github.com/GAMch1k/life-os-app/releases/latest/download/updates.json` (`lifeos.updatesRepo`) | assets of a release of that repository | none, ever |

Each source names only its own files: a GitHub manifest whose `url` is anything but an APK asset of a release of that repository (another repository, another site, plain http, an `/android/` path, the other app's APK) is refused before a byte is downloaded, and so is a GitHub address in the official server's manifest. GitHub's downloads redirect (github.com → the release's asset → `release-assets.githubusercontent.com`); the app follows at most 5 redirects, only to github.com downloads of that repository's releases and GitHub's asset hosts (`release-assets.githubusercontent.com`, `objects.githubusercontent.com`), and never sends a token there. Nothing is installed from any other domain. A build of the app for another server and another key sets `lifeos.updatesUrl` and `lifeos.updatesRepo` to its own (`life-os-app`'s README, "Servers").

## release.json: what the build writes

A small contract between the app repository's build and the publish script. One object per app, under `phone` and `wear` (either may be missing; both may also sit under a top-level `apps`):

What `tools/build` in `~/life-os-app` writes (`dist/release.json`):

```json
{"schema": 1, "apps": {
  "phone": {"file": "lifeos-phone-0.3.0-3.apk", "applicationId": "com.gamch1k.lifeos", "versionCode": 3, "versionName": "0.3.0", "minSdk": 34, "sha256": "acfd…", "size": 12085080, "builtAt": "2026-10-12T19:40:00Z", "gitCommit": "b18a6d1…"},
  "wear": {"file": "lifeos-wear-0.3.0-3.apk", "applicationId": "com.gamch1k.lifeos", "versionCode": 3, "versionName": "0.3.0", "minSdk": 33, "sha256": "b684…", "size": 15829290, "builtAt": "2026-10-12T19:40:00Z", "gitCommit": "b18a6d1…"}
}}
```

The script reads `gitCommit` as `commit` (either name works) and ignores `schema`, `file`, and `applicationId`; the APK files are found by their versioned names.

| Field         | Required | Form                                                                                                     |
| ------------- | -------- | -------------------------------------------------------------------------------------------------------- |
| `versionCode` | yes      | whole number, 1 to 2100000000 (Android's limit); must grow with every release                            |
| `versionName` | yes      | 1–64 letters, digits, `.`, `_`, `+`, `-`, starting with a letter or digit; part of the APK's file name   |
| `minSdk`      | yes      | whole number, the APK's minimum API level                                                                |
| `sha256`      | yes      | the APK's SHA-256, 64 lowercase hexadecimal digits (`sha256sum`)                                         |
| `size`        | yes      | the APK's size in bytes                                                                                  |
| `builtAt`     | yes      | RFC 3339 time of the build, such as `2026-10-12T10:00:00Z`                                               |
| `commit`      | yes      | the app repository's commit, 1–64 letters, digits, `.`, `_`, `+`, `-` (such as `0123abc` or `0123abc-dirty`) |

Other keys are ignored. Should the build ever write other names, the publish script maps them in one place (the `release=$(jq …)` filter near its top), not here.

## The manifest: updates.json

Served at `/android/updates` (and `/android/updates.json`) as `application/json` with `Cache-Control: no-cache`. The publish script writes it; nothing else does.

```json
{
  "formatVersion": 1,
  "updatedAt": "2026-10-12T14:03:00Z",
  "apps": {
    "phone": {
      "versionCode": 3,
      "versionName": "0.1.2",
      "minSdk": 34,
      "sha256": "9f2c…",
      "size": 18234567,
      "url": "/android/lifeos-phone-0.1.2-3.apk",
      "publishedAt": "2026-10-12T14:03:00Z",
      "builtAt": "2026-10-12T10:00:00Z",
      "commit": "0123abc",
      "notes": "Tasks open from notifications",
      "previous": [
        {"versionCode": 2, "versionName": "0.1.1", "minSdk": 34, "sha256": "…", "size": 18100000, "url": "/android/lifeos-phone-0.1.1-2.apk", "publishedAt": "…", "builtAt": "…", "commit": "…", "notes": ""}
      ]
    },
    "wear": {"versionCode": 3, "versionName": "0.1.2", "…": "…"}
  }
}
```

- `formatVersion` is 1; a change that breaks readers raises it.
- `updatedAt` is when the last publish changed the manifest (UTC); `publishedAt` is when that app's version was published.
- `url` is a path on the same host (the public URL, such as `https://lifeos.example.com`, from the app; the WireGuard address, such as `http://10.8.0.1:3000`, over WireGuard). A URL's file never changes content. In a GitHub release's `updates.json` it is instead that APK's download address in the release ([GitHub Releases](#github-releases)).
- `versionCode`, `versionName`, `minSdk`, `sha256`, `size`, `builtAt`, `commit` are copied from `release.json`; `notes` is the publish's `--notes`, or empty.
- `previous` lists the up to 4 versions kept before the current one, newest publish first, in the same form. The app does not need them.
- An app that was never published is absent from `apps`.

What the app does with it: if `apps.phone.versionCode` is greater than its own and the device's API level is at least `minSdk`, and `url` is an address of the source the manifest came from, download `url` (it may resume with a `Range` request), refuse it unless its size is `size` and its SHA-256 is `sha256`, and unless it is this app, at that versionCode, signed by the installed app's own certificate, then install it.

## What the gateway serves

`location /android/` in `infra/nginx/life-os.conf.template`, on the Life OS server only, behind the same Access requirement as everything else:

| Path                                                       | Serves                       | Content type                                | Caching                                 |
| ---------------------------------------------------------- | ---------------------------- | ------------------------------------------- | --------------------------------------- |
| `/android/`                                                | `index.html`, the download page | `text/html; charset=utf-8`               | `no-cache`                              |
| `/android/updates`, `/android/updates.json`                | `updates.json`, the manifest | `application/json`                          | `no-cache`                              |
| `/android/lifeos-(phone\|wear)-<versionName>-<versionCode>.apk` | the APK, as an attachment (`Content-Disposition: attachment`; ranges supported) | `application/vnd.android.package-archive` | `private, max-age=31536000, immutable` |
| `/android`                                                 | a redirect to `/android/`    |                                             |                                         |

Every other path under `/android/` is a 404 (there is no directory listing, and no other file is served, `release.json` included); a method other than GET or HEAD is a 405; a symbolic link in the directory is refused (403). nginx resolves `.` and `..` segments before it picks a location, so `/android/../…` leaves `/android/` (and reaches the Dashboard) instead of the directory, and encoded ones that climb above the root are a 400. The download page and the manifest are revalidated on every load (an `ETag` makes that cheap); an APK's name carries its version, so it is cached for good, but only by the phone: `private` keeps Cloudflare, which caches `.apk` files by default, from storing a copy at its edge. Downloads use the general API rate limit (30 requests a second per client, a burst of 60): a download is one request, however long, and `send_timeout` (60 s) bounds a pause in it, not its length.

## Keeping old versions

The newest 5 versions of each app stay in the directory: the current one and the 4 in its `previous` list, newest publish first. A publish deletes every APK the new manifest no longer lists, and the page lists the kept older versions.

Android does not install an older `versionCode` over a newer one (only after uninstalling, which loses the app's data on the phone). So a bad release is rolled back by publishing a fixed build with a higher `versionCode`. The older APKs are for reference: a fresh install of an older build, or a comparison. `--force` publishes a lower or equal `versionCode` (the manifest then names it as current, for fresh installs); installed apps keep their newer version.

## Security model

- **Behind Access, never public.** `/android/` is a path of the public hostname (`LIFE_OS_PUBLIC_HOSTNAME`) like the Dashboard: Cloudflare Access's login comes first, the connector validates the Access token, and the gateway refuses a tunnel request without one ([Cloudflare Tunnel and Access](cloudflare.md#origin-trust-and-access-paths)). There is no bypass for the page, the manifest, or the APKs. The app reaches them through Access, like its other Life OS requests. Over WireGuard (the gateway's WireGuard address, such as `http://10.8.0.1:3000/android/`) the usual peer trust applies.
- **The signer is pinned, by the app and by Android.** Before the installer, the app checks that the APK is its own package at the announced version, newer than the installed one, and signed by exactly the installed app's certificate (`ApkRules`; the phone and watch apps share one key, see the `life-os-app` README); then the package installer checks the signature again. A replaced or tampered APK, on the server or on GitHub, cannot take over the installed app, and a downgrade is refused by both. The publish script handles signed APKs only, and uploads nothing else to GitHub.
- **The manifest's `sha256` and `size`** let the app refuse a broken, truncated or swapped download before it reaches the installer, from either source; the page and the GitHub release notes show the SHA-256 for checking a first install by hand.
- **The first install** trusts the download page, which only the Access-authenticated owner reaches, or for anyone else the GitHub release, which only accounts with push access to the app repository can publish. Check its certificate before installing: `apksigner verify --print-certs lifeos-phone-….apk` must show the certificate SHA-256 in the `life-os-app` README ("Signing").
- **GitHub only gets what is public anyway**: the signed APKs, their manifest and the notes. `/android/` stays behind Access; nothing about the owner's server goes to GitHub.
- **The gateway only reads.** The directory is mounted read-only; only the page, the manifest, and APK names are served; symbolic links are refused; nothing in the directory executes. The publish script refuses a `versionName` that is not a plain file name part, so a `release.json` cannot place a file outside the directory.
- **The verification stack never sees the owner's releases.** `tools/verify-compose` mounts a scratch directory of its own (`LIFE_OS_VERIFY_ANDROID_RELEASES_DIR`, by default an empty one under `TMPDIR`) and refuses a plan that mounts the owner's.

## Checks

- `tools/test-publish-android-release` (no Docker, no network, scratch directories only; in CI's `check-generated` job): a release publishes, the same release again publishes nothing, a downgrade and a SHA-256 or size mismatch are refused with nothing changed, invalid releases and directories are refused, the newest 5 versions are kept, and a publish killed midway (at an APK's rename and at the manifest's) leaves the old manifest and its APKs intact. The GitHub step runs against a made-up GitHub (`tools/fixtures/fake-gh` on `PATH` stands in for gh, so the test never reaches the real one): the release is created as a draft at the build's commit with both APKs and an `updates.json` that describes them, then published as the latest; the same release again changes nothing; `--dry-run` changes nothing anywhere; a commit not on GitHub, a `-dirty` build (even with `--force`), a short commit, one app only, two versions, a downgrade below GitHub's latest, the same tag with other APKs, no push access, and a logged-out gh are all refused with nothing changed here or there; a publish whose GitHub step failed is finished by running it again; and an upload GitHub stored wrongly is never published.
- `life-os-app`'s unit tests (its CI): the app's choice of source, GitHub's redirects followed only to GitHub, no token on GitHub, and refusals of another repository, another site, a wrong checksum or size, another signer, and a downgrade.
- `tools/test-android-gateway` (throwaway containers of the gateway's image, never the deployed gateway): `nginx -t`; the manifest, page, and APK headers; a 20 MB APK arrives whole and by range; missing files, other names, symbolic links, other methods, and traversal get nothing; and on the Cloudflare path `/android/` needs Access's token.
- `apps/dashboard/e2e/android-updates.spec.ts`, run by `tools/browser-e2e`, which publishes a made-up release (`tools/fixtures/android-release`) into the verification stack's own directory: the manifest, both APKs, and the page with its download.

## Troubleshooting

| Symptom | Likely cause | Fix |
| --- | --- | --- |
| `/android/updates` is 404 | Nothing published yet, or the gateway was not recreated with the mount | Publish; `docker compose exec gateway ls -l /srv/android` must list the files |
| `/android/` is the Dashboard's 404 page | The gateway runs the old configuration | `docker compose up -d --no-build --no-deps --force-recreate gateway` |
| 403 for every file | The gateway cannot read them | `chmod 755` the directory; the script writes files 644 |
| `the releases directory … is not writable` | Docker created it, owned by root | `sudo chown "$(id -u):$(id -g)" ~/life-os.android` (or your `LIFE_OS_ANDROID_RELEASES_DIR`) |
| `refusing a downgrade` / `already published with a different APK` | The build's `versionCode` did not grow | Bump `versionCode` in the app and build again (`--force` only on purpose) |
| `the SHA-256 of … is …` | The APK changed after `release.json` was written, or the copy is incomplete | Build again, or copy the release directory again |
| `the published manifest … is unreadable` | `updates.json` was edited by hand | Repair it, or remove it to start over (the next publish then deletes the APKs it no longer lists) |
| `the build's commit … is not on GitHub` | The app repository's commit was not pushed | `git -C ~/life-os-app push`, then publish again |
| `a GitHub release carries both apps` / `… differ in versionName, versionCode, or commit` | A release directory of one app, or of two builds | Build both with `tools/build`, or publish it here only with `--no-github` |
| `gh api … failed` / `gh does not see the repository` / `may not publish releases` | gh is not logged in, or as an account without push access | `gh auth status`; `gh auth login` as the repository's owner |
| `is not above the latest GitHub release` | GitHub already has this or a newer versionCode | Build with a higher `versionCode` (`--force` only on purpose) |
| `the GitHub release … is not published (see above); everything here is` | The GitHub step failed after the server's part | Run the same command again |
| An app on another server says the update check failed with a 404 | Nothing is released on GitHub yet | Publish once with the GitHub step |
