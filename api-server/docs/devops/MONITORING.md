# Monitoring & alerting (Telegram — zero external services, zero cost)

Two independent layers. Both post to the same Telegram group; both are free
(the Telegram Bot API has no charges, ever).

| Layer | Catches | Runs |
|---|---|---|
| **In-app error alerts** (`pkg/alerting`, zerolog hook) | any `Error`-level log event anywhere in the API — payment failures, DB errors, panics that get logged | inside the api process |
| **External uptime ping** (`.github/workflows/uptime.yml`) | the box/nginx/DNS being DOWN — the failure on-box alerting can never report | GitHub Actions, every ~5 min |

## One-time setup (~10 minutes)

1. **Create the bot**: message `@BotFather` in Telegram → `/newbot` → name it
   (e.g. `Rides Alerts`) → copy the **bot token**.
2. **Create the team group**: make a Telegram group, add the bot **and** the team.
3. **Get the chat id**: send any message in the group, then open
   `https://api.telegram.org/bot<TOKEN>/getUpdates` in a browser and read
   `"chat":{"id":-100XXXXXXXXXX}`. Group ids are negative — keep the minus sign.
4. **Box env** (in `/opt/rides/Rides-api/api-server/.env`, then
   `docker compose -f docker-compose.prod.yml up -d api`):
   ```
   TELEGRAM_BOT_TOKEN=123456:ABC-...
   TELEGRAM_CHAT_ID=-100xxxxxxxxxx
   ```
5. **Repo secrets** (GitHub → Settings → Secrets and variables → Actions) for
   the uptime workflow: `TELEGRAM_BOT_TOKEN`, `TELEGRAM_CHAT_ID` (same values).

Unset env/secrets = both layers silently disabled (dev default).

## Flood protection (in-app layer)

- **Per-message dedupe**: the same error repeating (crash loop) alerts once per
  10 minutes, not per occurrence.
- **Global cap**: max 20 alerts per sliding hour across all messages.
- **Never blocking**: sends are async on a bounded queue; when Telegram is slow
  or the queue is full, alerts are dropped — alerting can never slow a request
  or take the app down.

A `🚀 Rides API starting` notice is sent on every boot — doubling as a deploy
signal in the group.

## Testing it

```bash
# after setting the env on the box, restart api and watch the group for 🚀
# force an error alert:
docker compose -f docker-compose.prod.yml exec api sh -c 'kill -0 1'  # (any op) —
# or simpler: hit an endpoint that logs an error, or temporarily break a config.
# uptime workflow: Actions → Uptime check → Run workflow (manual dispatch).
```
