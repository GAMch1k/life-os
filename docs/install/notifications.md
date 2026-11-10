# Notifications

Life OS tells you about things: the morning summary of your day, task and event reminders, food that expires tomorrow, a bank access that ends soon, a capture that needs you in Human Review, and problems on the Status page. Every notification is listed under **Notifications** in Life OS. By default that is all: nothing leaves your server.

To get them on your phone, choose one of two ways, or both.

## The Life OS phone app

The [Life OS Android app](phone-apps.md) shows notifications as pushes, even while it is closed.

```sh
./setup.sh webpush
./setup.sh up
```

It makes the key pair that signs the pushes and turns the channel on. Then, in the app, turn notifications on (Settings). The pushes travel through the phone's push service (Google's FCM), encrypted end to end, so Google sees only that a push arrived.

Without an https address, the script asks for your email address: push services want a way to reach the sender. Keep the key pair (it is in `.env`): a new one makes every phone turn notifications on again.

The app itself needs Life OS behind Cloudflare Access today: see [Phone and watch apps](phone-apps.md).

To send a test push: `curl -X POST http://127.0.0.1:3000/api/notifications/v1/push/test -H 'X-Life-OS-Actor: user'`.

## ntfy

[ntfy](https://ntfy.sh/) is an open-source notification app. Life OS runs its own ntfy server, private to you. It needs the [Cloudflare Tunnel](access.md#cloudflare-tunnel-and-access): ntfy gets a second hostname in the same tunnel, without Cloudflare Access (the ntfy app can't log in through Access). ntfy itself refuses everyone except two accounts: Life OS, which may only post, and your phone, which may only read.

1. In Cloudflare Zero Trust, add a second **published application route** to your tunnel: a hostname such as `ntfy.example.com` → **HTTP** → **`ntfy:80`**. Don't add an Access application for it, and don't turn on Access JWT validation on this route.
2. On the server:

   ```sh
   ./setup.sh ntfy
   ./setup.sh up
   ```

   It asks for ntfy's address (`https://ntfy.example.com`) and a password for your phone (at least 12 characters; keep it in your password manager). It stores only the password's hash.

3. On your Android phone, install **ntfy** from Google Play or F-Droid. In its settings, add a user: server `https://ntfy.example.com`, username `lifeos-phone`, and your password. Then subscribe to the topic `lifeos` on that server.

More, including how to check that the ntfy hostname can't reach Life OS: [ntfy](../operations/ntfy.md).

## When notifications come

Reminder times are in `.env`, in your timezone: the morning summary (`LIFE_OS_TASKS_SUMMARY_TIME`, 08:00), tasks due today (`LIFE_OS_TASKS_MORNING_TIME`, 09:00) and tomorrow (`LIFE_OS_TASKS_EVENING_TIME`, 19:00), a task with a time 30 minutes before (`LIFE_OS_TASKS_TIMED_LEAD_MINUTES`), and food expiring (`LIFE_OS_FOOD_EXPIRY_TIME`, 08:00). After a change, run `./setup.sh up`.
