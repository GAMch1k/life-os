# Calendar feeds (ICS)

Many calendars are published as a feed, an address ending in `.ics` or starting with `webcal://`: public holidays, school terms, a sports team's games, waste pickup days, or your own calendar from another service. Life OS can subscribe to any number of them. Their events show in Calendar with the feed's name, read-only, and are refreshed every night at 04:30 (`LIFE_OS_CALENDAR_SOURCE_REFRESH_AT`) and when you add one.

## Add a feed

Life OS must be running. On the server:

```sh
./setup.sh feed
```

It asks for a name, such as "Holidays", and the feed's address, and adds it. The first download starts at once.

Only feeds on the internet work: `https://` (or `webcal://`, which is the same) on a public address. Life OS refuses feeds on your own network, so a feed can't be used to reach your other machines.

Some feed addresses are secret, such as the private iCal address of a calendar at Google or Outlook: anyone with it can read that calendar. Life OS keeps the address in its database (and so in the backups) and never shows it in its logs.

## Reminders and priority

In **Calendar → Reminder defaults**, each feed has its own section: the reminders its events get, and their priority. You can also change one event's reminders and priority in its details.

## List, rename, or remove feeds

There is no page for this in the Dashboard yet; use the API on the server:

```sh
# Every feed, with its id, its last refresh, and any error:
curl -s http://127.0.0.1:3000/api/integrations/calendar-feeds/v1/feeds

# Remove one, with its events:
curl -X DELETE -H 'X-Life-OS-Actor: user' \
  http://127.0.0.1:3000/api/integrations/calendar-feeds/v1/feeds/<id>
```

Use your gateway's address and port if you changed them. The [calendar-feeds API](../services/calendar-feeds.md#api) has the rest, such as a filter that keeps only some events.

## Waste collection in Darmstadt-Dieburg (ZAW)

Life OS has a preset for the ZAW Abfallkalender in the Darmstadt-Dieburg district (Germany): set `LIFE_OS_ZAW_ICS_URL` in `.env` to your street's MyMüll iCal address, and run `./setup.sh up`. It keeps a "ZAW" calendar with a reminder the evening before each pickup. See [ZAW waste collection](../services/calendar-feeds.md#zaw-waste-collection). Elsewhere, your waste company's own feed works as an ordinary feed.
