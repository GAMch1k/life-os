# Email

Life OS can send email through your own email provider. It is optional and off by default: without it, Life OS works as before, and no page offers anything by email.

With email on, Life OS sends **account emails**:

- a link to **verify your email address** (Settings → Email);
- a link to **set a new password** when you forgot it ("Forgot password?" on the sign-in page);
- a **sign-in code by email**, as a second factor next to the authenticator app and passkeys;
- **security alerts**, such as a sign-in from a new device or a new API token.

Notifications by email (reminders, a daily summary) come later.

Life OS sends through any provider that offers SMTP: Resend, Postmark, Brevo, Mailgun, Amazon SES, Fastmail, Gmail with an app password, or your own mail server. The notifications service does the sending. The password stays in `.env`: never in the database, a log, or `./setup.sh status`.

## Set it up

On the server, in the directory with `setup.sh`:

```sh
./setup.sh email
```

It asks for:

1. the SMTP server, such as `smtp.resend.com`;
2. the TLS mode: `starttls` (port 587, the default), `tls` (port 465), or `none` (a relay on your own network);
3. the port (the mode's usual one is the default);
4. the user name, and then the password or API key (hidden as you type; empty keeps the current one);
5. the From address, one your provider lets you send from, such as `lifeos@example.com`;
6. the From name (default `Life OS`).

It writes them into `.env`, restarts the notifications service alone (`--no-deps`), and asks for an address to send a test email to. It says whether the test went out, or why not, in plain words.

| Command | What it does |
| --- | --- |
| `./setup.sh email` | Set up or change the settings, then send a test email. |
| `./setup.sh email test you@example.com` | Send a test email again, with the settings Life OS runs with. |
| `./setup.sh email off` | Turn email off: the settings in `.env` become empty, and the notifications service restarts. |
| `./setup.sh status` | The **Email** line: not set up, works (when it last sent), nothing sent yet, failing since when and why, or settings that are wrong. |

The **Status** page in Life OS has an **Email** line too: off, works, or failing since when.

Without questions (for a script), give the settings as options; the password comes from `LIFE_OS_SETUP_SMTP_SECRET` in the environment, never as an option, which other users of the server could see:

```sh
LIFE_OS_SETUP_SMTP_SECRET='re_…' ./setup.sh email --yes \
  --smtp-host smtp.resend.com --smtp-tls tls --smtp-user resend \
  --smtp-from lifeos@example.com --test-to you@example.com
```

`--smtp-port`, `--smtp-from-name`, and `--smtp-user=` (no user) work too. Without `--test-to`, it sends no test email.

In a clone of the repository, `./setup.sh email` works the same; it recreates the service with `docker compose up -d --no-build --no-deps notifications`. By hand: set the settings below in `.env`, then run that command.

## The settings

| Setting | What it is |
| --- | --- |
| `LIFE_OS_SMTP_HOST` | The SMTP server. Empty: email is off. |
| `LIFE_OS_SMTP_PORT` | Its port. Empty: 465 for `tls`, 587 for `starttls`, 25 for `none`. |
| `LIFE_OS_SMTP_TLS` | `starttls` (empty), `tls`, or `none`. `starttls` and `tls` always encrypt: Life OS never falls back to plain text. `none` is for a relay on your own network, and then without a user or password. |
| `LIFE_OS_SMTP_USERNAME` | The user name, if the server needs one. |
| `LIFE_OS_SMTP_SECRET` | The password or API key. A secret: keep it out of everything but `.env`. |
| `LIFE_OS_SMTP_FROM` | The From address. |
| `LIFE_OS_SMTP_FROM_NAME` | The From name. Empty: `Life OS`. |

## Providers

| Provider | Server | TLS and port | User | Password |
| --- | --- | --- | --- | --- |
| Resend | `smtp.resend.com` | `tls` 465, or `starttls` 587 | `resend` | an API key |
| Postmark | `smtp.postmarkapp.com` | `starttls` 587 | the Server API token | the same token |
| Brevo | `smtp-relay.brevo.com` | `starttls` 587 | your SMTP login | an SMTP key |
| Mailgun | `smtp.mailgun.org`, or `smtp.eu.mailgun.org` in the EU | `starttls` 587 | the domain's SMTP user, such as `postmaster@mg.example.com` | its SMTP password |
| Amazon SES | `email-smtp.<region>.amazonaws.com` | `starttls` 587, or `tls` 465 | the SES SMTP user name | the SES SMTP password (not your AWS access keys) |
| Fastmail | `smtp.fastmail.com` | `tls` 465 | your address | an app password |
| Gmail | `smtp.gmail.com` | `tls` 465, or `starttls` 587 | your Gmail address | an app password |
| A local relay | its host name, such as `postfix` | `none` 25 | none | none |

- **Amazon SES** starts in a sandbox that sends only to addresses you verified: ask AWS to leave it before others get email from Life OS.
- **Gmail** needs 2-Step Verification on your Google account before it offers app passwords. The From address must be your Gmail address (or one you added to it).
- **A local relay** is your own Postfix, or a relay container on the same Docker network as Life OS. Life OS sends it no password, since without TLS the password would cross the network readable.

**SPF and DKIM.** Send from an address at a domain whose DNS has your provider's SPF and DKIM records. Without them, mail servers take Life OS's emails for forgeries: they land in spam, or bounce. Your provider's setup page shows the records to add, and checks them. A DMARC record is good to have, but optional.

## What Life OS sends

Every email is plain text with a simple HTML version, in English, with your Life OS's address in it. No tracking pixels, no remote images. Life OS's log keeps what kind of email it was and whether it went out, never an address or the content.

**Emails you ask for.** They go out only when you ask, so they cannot be turned off:

| Email | What it holds |
| --- | --- |
| Verify your address | A link that works for 24 hours. Settings → Email shows your address as **not verified · Send link** until you open it. Signing in never needs it; it unlocks the emails below. |
| Set a new password | "Forgot password?" on the sign-in page: enter your username or email address. A link goes to the account's **verified** address; Life OS answers the same whether or not such an account exists. The link works once, for 30 minutes. Setting the new password signs out every session of the account, its paired phones and watches, its API tokens, and its connected apps, as `./setup.sh recover-owner` does. Two-factor sign-in stays on. |
| Sign-in code | Turn it on in Settings → Security (**Sign-in code by email**), with a verified address. When you sign in, Life OS emails a 6-digit code instead of asking for the authenticator app's. A code works for 10 minutes, with 5 tries. The authenticator app stays the better choice: anyone who gets into your mailbox gets the codes too. |

Sign-in codes by email don't open the admin pages: those still need the authenticator app or a passkey ([The server's admin](accounts.md#the-servers-admin)).

**Security alerts.** They come without your asking, to your verified address. Each kind turns off in **Settings → Email**; only you can change your own switches, admins cannot. Each is in the [security log](accounts.md#settings--security-log) too.

- **Sign-in from a new device:** your password was used in a browser that never signed in to your account before. It comes also when the second factor stopped the sign-in: then someone knows your password, so change it.
- **Password or email address changed**, or set with a reset link.
- **Two-factor sign-in changed:** the authenticator app or sign-in codes by email turned on or off, new recovery codes, a passkey added or removed.
- **New API token, paired device, or connected app.**
- **Legacy access turned on** (`./setup.sh legacy-access on`).
- **Signed out everywhere:** by you, or for everyone with `./setup.sh sign-out-all`.

**Limits.** So that nobody can flood a mailbox (or your provider's account) through Life OS:

- "Forgot password?": 3 links an hour per address, and 10 requests an hour from one network address;
- sign-in codes: one every 30 seconds, and 5 every 15 minutes, per account; wrong codes count as [failed sign-ins](accounts.md#failed-sign-ins);
- verification links: 5 an hour per account;
- new-device alerts: 10 an hour per account;
- the whole server sends at most 30 emails a minute and 300 an hour.

## Turning it off

`./setup.sh email off` empties the settings and restarts the notifications service; Life OS stops offering everything above. An account whose only second factor is the sign-in code by email then signs in with a recovery code, if it has some, or the owner on the server with `./setup.sh recover-owner --reset-2fa` ([Locked out](accounts.md#locked-out)). Turn the authenticator app on before you turn email off.

## Troubleshooting

| What you see | What to do |
| --- | --- |
| `email_auth_failed`: the server refused the user name or password | Check the user and password. Gmail and Fastmail need an app password, not your account's password; Amazon SES its SMTP credentials, not your AWS keys. |
| `email_connect_failed` or `email_timeout` | Check the server name, the port, and the TLS mode (465 goes with `tls`, 587 with `starttls`). Many hosting providers block outgoing ports 25, 465, or 587: ask yours, or try the provider's other port. |
| `email_tls_failed` | The TLS mode does not fit the port, or the server's certificate is not valid for its name. |
| `email_sender_refused` | The From address is not one your provider lets you send from: verify the address or domain with the provider. |
| `email_recipient_refused` | The provider refused the address you sent to (a typo, or the SES sandbox). |
| The test email arrives in spam, or not at all | Add the provider's SPF and DKIM records to your domain's DNS (above). |
| `./setup.sh status` says **FAILING since …** | The last emails did not go out, for the reason it shows. Fix the settings with `./setup.sh email`; a working send clears it. |
| `./setup.sh status` says **settings are wrong** | `.env` holds settings that cannot work, such as a TLS mode other than the three, or a user with `none`: run `./setup.sh email` again. Life OS keeps running, without email. |
