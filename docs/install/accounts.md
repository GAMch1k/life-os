# Accounts and signing in

Life OS has accounts and a login. The first account is the server's **owner**; it owns everything that was in Life OS before. Second accounts (family members) come with a later version: until then the owner's account is the only one, and nobody can sign up.

How it works inside is in [Multi-user design](../architecture/multi-user.md); this page is what you do with it.

## The first account

The first account needs a one-time **setup code** from the server. Whoever can run commands on the server owns Life OS: that is the trust root, and also the way back in when you are locked out ([Locked out](#locked-out)).

1. On the server, in the directory with `setup.sh`:

   ```sh
   ./setup.sh setup-code
   ```

   It prints a code such as `K7QM-2XRP-9VDA-H4TC`. `./setup.sh up` and `./setup.sh update` print one by themselves while no account exists. A code works once, for 24 hours; a new one replaces the old one. A sign-up that is refused for its input (a password that is too short, say) leaves the code working; once an account is made with it, it is used up, and of two sign-ups at the same moment only one gets the account.

2. Open Life OS at its HTTPS address (or `http://localhost:3000` through an SSH tunnel), go to **/setup**, and enter the code, a username, and a password of at least 10 characters. A name and an email address are optional: the email is only shown, never used to sign in or to send anything.

3. Life OS signs you in and opens **Settings → Security**. Turn on two-factor sign-in there next ([Two-factor sign-in](#two-factor-sign-in)): signed in, the owner needs it before Status shows its details and the admin pages open ([The server's admin](#the-servers-admin)).

In a clone of the repository (the reference server), the same command runs in the Dashboard's container: `docker compose exec -T dashboard node scripts/lifeos-auth.mjs setup-code`.

## Signing in

**/login** asks for the username and the password, then, with two-factor on, for the 6-digit code of your authenticator app (or one of your recovery codes). With a passkey you sign in with your phone's or computer's screen lock alone. A session lasts 30 days from its last use, in that browser.

Signing in works only where the browser can keep the session cookie safe: at an `https://` address, or at `localhost` and `127.0.0.1` (an SSH tunnel). At a plain-HTTP address, such as a WireGuard address `http://10.8.0.1:3000`, the login page says to use the HTTPS address instead, because anyone on the way could read the password and the session. To allow it anyway on such a host, list it in `LIFE_OS_INSECURE_LOGIN_HOSTS` in `.env` (comma-separated, such as `10.8.0.1`) and recreate the Dashboard. Passkeys never work there.

Usernames are not case-sensitive. After five wrong passwords for one username from one address, the next attempt from there waits 15 seconds, then twice as long after each further failure, up to 15 minutes; the gateway and Better Auth also limit each address. The person, signing in from elsewhere, is not slowed down.

## Settings → Security

The person icon in the header opens **Settings → Security** (`/settings/security`):

| Section | What you can do |
| --- | --- |
| **Authenticator app** | Turn on two-factor sign-in with an authenticator app (scan the QR code, or type the key), get ten recovery codes, make new ones, or turn it off. Each needs your password. |
| **Passkeys** | Add a passkey (your phone's or computer's screen lock), or remove one. Passkeys work at the public address (`LIFE_OS_PUBLIC_URL`) only. |
| **Password** | Change it; every other session is signed out. |
| **Where you are signed in** | Every session, with its browser and last use: sign one out, or every one but this. |
| **Legacy access** | The owner only: turn [legacy access](#legacy-access) off. Turning it on is done on the server. |

Adding a passkey needs a sign-in from the last 12 hours; Life OS asks you to sign in again otherwise.

### Two-factor sign-in

An authenticator app (Google Authenticator, Aegis, 1Password, and others) shows a new 6-digit code every 30 seconds. Turn it on, scan the QR code, enter the code the app shows, and keep the ten **recovery codes** somewhere safe, such as a password manager: each signs you in once if your phone is not at hand. A passkey counts as two-factor too.

## Settings → Security log

**Settings → Security log** (`/settings/security-log`) lists what concerns your account, newest first: every sign-in (with a password, an authenticator code, a recovery code, or a passkey), every failed one, sign-outs, password and two-factor and passkey changes, and what an admin or the server did to the account. Each entry says when, what, who did it, and from which kind of device and address. It never holds anything you wrote: no titles, amounts, or messages, and not the username someone typed. Entries are kept for a year. A failed sign-in you do not recognise means someone tried your password: change it, and turn on two-factor sign-in if it is off.

The owner's log also has legacy access's use: one entry a day for each address and kind of device that used Life OS without signing in, with the number of requests.

## The server's admin

The owner, and later the admins the owner names, look after the server; they never see anyone's content. With an account signed in, the admin's parts need two-factor sign-in on it:

- **Status** shows every check and its details (the server's memory and disks, the containers, the backups, the AI plan's usage) and **Back up now**. Anyone else, and an admin without two-factor sign-in, sees one line for the modules they use: "Everything works." or "Calendar has a problem.".
- **All accounts' log** (`/admin/security-log`): the security log of every account, filtered by account, kind, and days. It also has the commands run on the server (`setup-code`, `recover-owner`, `legacy-access`, `sign-out-all`, a rotated key) and every admin action, refused ones too.
- **Diagnostics** (`/admin/diagnostics`): the services' server errors, counted by what went wrong and where (codes, never content), over the last day and week; kept 90 days.

Settings' pages link to them for the owner and admins. While [legacy access](#legacy-access) is on, a request without a sign-in still gets Status and Back up now as before; the admin pages always need a real sign-in.

## Legacy access

Before accounts, whoever reached Life OS was its owner. **Legacy access** keeps it that way while it is on: a request without a sign-in acts as the first account, exactly as before. A request that does carry a sign-in is always that account's.

- It is **on** after updating an install from before accounts (`./setup.sh update` writes `LIFE_OS_LEGACY_ACCESS=on` into `.env`), so that nothing changes for you, your browsers, and the phone and watch apps, which send no Life OS sign-in yet.
- It is **off** on a new install.
- The pages that are new with accounts (Settings, and the admin pages) need a real sign-in all the same. Status's details and Back up now, which are older, stay open to legacy access as the owner.
- The [security log](#settings--security-log) counts its use: one entry a day for each address and kind of device, with the number of requests. Once the apps can be paired with an account (a later version), the Dashboard will remind you once a day while legacy access is on; until then it does not, since turning it off would cut the apps off.
- Turn it off in **Settings → Security** once the phone and watch apps are paired with your account (they learn to pair in a later version). From then on, everything needs a sign-in. On the server: `./setup.sh legacy-access off` (or no argument to see it).
- Turning it **on** is done on the server only: `./setup.sh legacy-access on`. It turns sign-in off for the whole server, so a browser cannot do it, not even the owner's (an old or stolen session must not be able to). It is refused while more than one account exists.

`LIFE_OS_LEGACY_ACCESS` in `.env` counts only once, the first time the Dashboard uses its database; afterwards the switch in Settings (off) and the command (on or off) decide.

## Locked out

- **Forgot the password:** on the server, `./setup.sh recover-owner` prints a one-time link to set a new one (`https://…/reset/<code>`). It works once, for 24 hours, and signs out every session of the account.
- **Lost the authenticator app and the recovery codes:** `./setup.sh recover-owner --reset-2fa` does the same and also turns two-factor off; turn it on again afterwards.

## Sign everyone out

For an emergency (a stolen laptop, a leaked database copy, anything that looks like someone else got in): `./setup.sh sign-out-all` signs out every session of every account, yours too, at once; everyone signs in again with their password and two-factor. It then says whether [legacy access](#legacy-access) is on: while it is, anyone who reaches Life OS still acts as the owner without signing in, so it offers to turn it off too (with `--yes` it only says so; `./setup.sh legacy-access off` turns it off). `--keep-devices` will keep paired devices signed in once devices exist. With Cloudflare Access in front, end its sessions too: [Cloudflare → Sign everyone out](../operations/cloudflare.md#sign-everyone-out). If the keys in `.env` may have leaked as well, rotate them ([Keys](#keys)).

## Keys

`setup.sh` writes three secrets into `.env` at the first setup, and `./setup.sh update` adds them to an older `.env`:

| Setting | What it is |
| --- | --- |
| `LIFE_OS_AUTH_SECRET` | Signs the session cookies and encrypts the two-factor secrets in the database. A backup of the accounts database is of little use without it: **keep `.env` with your backups**. |
| `LIFE_OS_IDENTITY_SIGNING_KEY` | The Ed25519 private key with which the Dashboard signs the short identity tokens the gateway hands the services. Only the Dashboard has it. |
| `LIFE_OS_TRUSTED_KEYS` | The public keys the services accept tokens from: the signing key's (and during a rotation the previous one too). |

The Dashboard checks the pair when it starts: it signs a test token and verifies it with `LIFE_OS_TRUSTED_KEYS`. If the trusted keys do not list the signing key, or list its `kid` with another public key, it logs `Life OS's sign-in settings are wrong: …` with the reason and answers every request with an error until `.env` is fixed and the Dashboard recreated.

`./setup.sh rotate-keys` makes a new signing key: the services trust the new and the current key first, then the Dashboard signs with the new one; the older key is dropped at the next rotation. Rotate after a suspected leak; there is no schedule. In a clone of the repository it prints the two `docker compose` commands to run instead.

In a clone of the repository, which `./setup.sh update` leaves alone, `./setup.sh account-keys` adds what an `.env` from before accounts lacks: the accounts database's password, the three keys, `LIFE_OS_LEGACY_ACCESS=on`, and the two optional settings, empty. It never prints a value and changes nothing else: a setting that has a value keeps it, whatever its format; only empty settings and the example's `replace-with-…` placeholders get new ones. If openssl cannot make the key, it stops and writes nothing. By hand: an Ed25519 key from `openssl genpkey -algorithm ed25519`, written as a JWK with a `kid` (as `setup.sh`'s `identity_key_pair` does), `LIFE_OS_TRUSTED_KEYS='{"keys":[<the same JWK without "d">]}'`, and `LIFE_OS_AUTH_SECRET` from `openssl rand -hex 32`.

## Cloudflare Access

With the Cloudflare Tunnel, the gateway still requires Cloudflare Access's token on every request through the tunnel, as before. Life OS's login comes on top. Once you trust the login alone, `LIFE_OS_REQUIRE_CLOUDFLARE_ACCESS=off` in `.env` (then recreate the gateway) makes it the only gate on the tunnel; the Access application must then let the hostname through ([Cloudflare](../operations/cloudflare.md)).
