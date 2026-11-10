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

**Life OS has no login of its own yet.** Whoever can reach its gateway can read and change everything in it. That is how the preview is meant to be run, not a vulnerability: it listens only on `127.0.0.1`, and you reach it through an SSH tunnel, a VPN, or Cloudflare Access ([Access it safely](docs/install/access.md)). Never expose it straight to the internet. Ways to get past those boundaries, or to reach Life OS's data without them, are worth reporting.

## Verifying what you run

The release images on `ghcr.io/gamch1k` are signed with cosign (keyless) and carry build provenance and an SBOM as GitHub attestations. The release bundle's files have build provenance and a `SHA256SUMS` file. [Verifying an image](docs/operations/releases.md#verifying-an-image) shows the commands.
