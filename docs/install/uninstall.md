# Uninstalling

Run these from the directory with `setup.sh` and `compose.yaml`. Life OS's containers, networks, and volumes are all named `lifeos…`.

## Stop it, keep everything

```sh
docker compose down
```

This removes the containers and networks. Your data stays: the database, captured photos, and voice notes in their Docker volumes, plus `.env`, `backups/`, and `codex-home/`. `./setup.sh up` brings Life OS back as it was. (`docker compose stop` only stops the containers.)

To remove one optional module and keep the rest, turn it off instead: `./setup.sh modules off <id>` removes its containers and keeps its data ([Modules](modules.md)).

## Delete everything

> [!CAUTION]
> This deletes all your Life OS data. Make a final backup and copy it off the server first if you might want it back ([Backups](backups.md)).

1. **Disconnect what is connected**, while Life OS still runs: Google Calendar (Calendar → Google Calendar → Disconnect) and your bank (Finance → Disconnect). That revokes their access at Google and at the bank.
2. **Delete the containers, the volumes, and the images:**

   ```sh
   COMPOSE_PROFILES=ai,voice,google-calendar,banking,cloudflare,ntfy,server-health \
     docker compose down --volumes --rmi all --remove-orphans
   ```

   Every [module](modules.md) is named, also those that are off, so that their containers, volumes, and images go too. `--volumes` deletes the database, the captured photos, voice notes, the Whisper model, and ntfy's data. There is no undo.

3. **Delete the directory**, with `.env`, the backups, and the AI sign-in:

   ```sh
   cd ~ && rm -rf ~/life-os
   ```

   The backups were written by the backup container as your user, so you can delete them yourself.

4. **Outside the server**, remove what you set up for Life OS, if you like:
   - Cloudflare: the tunnel, its routes and DNS records, the Access application, and service tokens.
   - Google: the OAuth client or the whole Cloud project, and Life OS under [your Google account's connections](https://myaccount.google.com/connections).
   - Enable Banking: the application.
   - The phone and watch apps, and the ntfy app's subscription.
