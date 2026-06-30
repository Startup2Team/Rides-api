#!/usr/bin/env bash
#
# momo-provision.sh — generate a SANDBOX MTN MoMo API user + API key.
#
# This automates MoMo Developer "Step 3" (normally done in Postman): it mints an
# api_user (a UUID) and its api_key from your Collections subscription key, then
# prints them ready to paste into .env as MOMO_API_USER / MOMO_API_KEY.
#
# Sandbox only. In production MTN issues api_user/api_key via the portal after
# onboarding — drop those into the same env vars instead of running this.
#
# Usage:
#   MOMO_SUBSCRIPTION_KEY=xxxx ./scripts/momo-provision.sh [callback_host]
#   ./scripts/momo-provision.sh <subscription_key> [callback_host]
#
# callback_host defaults to api.rides.rw (host only, no scheme/path).
set -euo pipefail

BASE_URL="${MOMO_BASE_URL:-https://sandbox.momodeveloper.mtn.com}"
SUB_KEY="${MOMO_SUBSCRIPTION_KEY:-${1:-}}"
CALLBACK_HOST="${2:-${MOMO_CALLBACK_HOST:-api.rides.rw}}"

if [[ -z "$SUB_KEY" ]]; then
  echo "ERROR: set MOMO_SUBSCRIPTION_KEY (Collections, Primary) or pass it as arg 1." >&2
  exit 1
fi

# A v4 UUID becomes the api_user (its own X-Reference-Id).
if command -v uuidgen >/dev/null 2>&1; then
  API_USER="$(uuidgen | tr '[:upper:]' '[:lower:]')"
else
  API_USER="$(cat /proc/sys/kernel/random/uuid)"
fi

echo "Provisioning against: $BASE_URL"
echo "api_user (X-Reference-Id): $API_USER"
echo "callback host: $CALLBACK_HOST"
echo

# Step 3a — create the API user.
code="$(curl -s -o /tmp/momo_apiuser.out -w '%{http_code}' -X POST \
  "$BASE_URL/v1_0/apiuser" \
  -H "X-Reference-Id: $API_USER" \
  -H "Ocp-Apim-Subscription-Key: $SUB_KEY" \
  -H "Content-Type: application/json" \
  -d "{\"providerCallbackHost\":\"$CALLBACK_HOST\"}")"
if [[ "$code" != "201" ]]; then
  echo "ERROR: create api user failed (HTTP $code):" >&2
  cat /tmp/momo_apiuser.out >&2; echo >&2
  exit 1
fi

# Step 3b — generate the API key for that user.
code="$(curl -s -o /tmp/momo_apikey.out -w '%{http_code}' -X POST \
  "$BASE_URL/v1_0/apiuser/$API_USER/apikey" \
  -H "Ocp-Apim-Subscription-Key: $SUB_KEY")"
if [[ "$code" != "201" ]]; then
  echo "ERROR: create api key failed (HTTP $code):" >&2
  cat /tmp/momo_apikey.out >&2; echo >&2
  exit 1
fi

# Extract apiKey without requiring jq.
if command -v jq >/dev/null 2>&1; then
  API_KEY="$(jq -r '.apiKey' /tmp/momo_apikey.out)"
else
  API_KEY="$(sed -E 's/.*"apiKey"[[:space:]]*:[[:space:]]*"([^"]+)".*/\1/' /tmp/momo_apikey.out)"
fi
rm -f /tmp/momo_apiuser.out /tmp/momo_apikey.out

echo "✅ Provisioned. Paste these into your .env:"
echo
echo "MOMO_API_USER=$API_USER"
echo "MOMO_API_KEY=$API_KEY"
echo
echo "Then restart the API. Sandbox calls will now authenticate."
