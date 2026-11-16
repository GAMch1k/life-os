# Security policy

## Supported versions

Life OS is an early preview. Security fixes go into `master` and into the next preview release; older releases get no fixes.

| Version | Supported |
| --- | --- |
| The latest preview release (`v0.1.0-preview.N`) | Yes |
| `master` | Yes |
| Any older release | No: [update](docs/install/updating.md) |

The Android and Wear OS apps have their own policy in [GAMch1k/life-os-app](https://github.com/GAMch1k/life-os-app/security/policy).

## Reporting a vulnerability

Report it privately. Never open a public issue, pull request, or discussion about a vulnerability.

1. Open the repository's **Security** tab and choose **Report a vulnerability** ([direct link](https://github.com/GAMch1k/life-os/security/advisories/new)). This is GitHub's private vulnerability reporting: only you and the maintainer see the report.
2. Include what you can of:
   - the version (the release tag, or the commit you built from) and how you run it (the install bundle or a build from source);
   - the part that is affected: a service, the Dashboard, the gateway, `setup.sh`, an image;
   - what an attacker can do, and what they need first (for example network access to the gateway);
   - the steps to reproduce it, or a proof of concept;
   - logs or output, with passwords, tokens, keys, and personal data removed.

## What happens next

Life OS has one maintainer, who handles reports on a best-effort basis:

- You get an answer, usually within a week.
- The maintainer confirms the problem or explains why it isn't one, and keeps you informed in the report.
- A confirmed problem is fixed in `master` and in the next preview release, and published as a GitHub security advisory. You are credited, unless you prefer not to be.

Please don't disclose the problem publicly until a fix is released, or until you have agreed on a date in the report.

## The preview's known limits

**Life OS's login is new.** Accounts, sessions, two-factor sign-in, device keys, API tokens, and MCP's OAuth came in `v0.1.0-preview.7` ([Accounts](docs/install/accounts.md)). A way to get past them, to act as another account, or to reach Life OS's data without signing in is a vulnerability: please report it. Until the login has had more scrutiny, the preview still listens only on `127.0.0.1` by default, and you reach it through an SSH tunnel, a VPN, or Cloudflare Access ([Access it safely](docs/install/access.md)); never expose it straight to the internet.

These are how Life OS is meant to work, not vulnerabilities:

- **Legacy access.** After an update from before accounts, legacy access is on: a request without a sign-in acts as the owner, as before, until the owner turns it off ([Legacy access](docs/install/accounts.md#legacy-access)). A new install has it off.
- **Plain HTTP.** Signing in over plain HTTP works on this server's loopback (`http://127.0.0.1`, `http://localhost`, as through an SSH tunnel), where the session cookie has no `Secure` flag because Safari refuses it there, and on a LAN or VPN address only once the owner allows it (`LIFE_OS_INSECURE_LOGIN_HOSTS`, `./setup.sh gateway --allow-http-login`): there the password crosses that network unencrypted. Every other host gets the `Secure` `__Host-` cookie and never signs in over plain HTTP.
- **The server is the trust root.** Whoever can run commands on the server (or read its `.env`) can make a setup code, reset the owner's password, and turn legacy access on.

## Verifying what you run

The release images on `ghcr.io/gamch1k` are signed with cosign (keyless) and carry build provenance and an SBOM as GitHub attestations. The release bundle's files have build provenance and a `SHA256SUMS` file. [Verifying an image](docs/operations/releases.md#verifying-an-image) shows the commands.
