#!/usr/bin/env bash
# ─────────────────────────────────────────────────────────────────────────────
# Taravelis Admin — 2FA End-to-End Test Script
# Run from the api-server/ directory after the server is up.
# Requires: curl, jq
# ─────────────────────────────────────────────────────────────────────────────

set -euo pipefail

BASE="http://localhost:8080/api/v1"
EMAIL="admin@taravelis.com"
PASSWORD="Admin1234!"

ok()   { echo -e "\033[32m✓\033[0m $1"; }
fail() { echo -e "\033[31m✗\033[0m $1"; exit 1; }
sep()  { echo -e "\n\033[34m── $1 ──\033[0m"; }

# ─── 0. Health check ──────────────────────────────────────────────────────────
sep "0. Health check"
STATUS=$(curl -s "$BASE/../health" | jq -r '.data.status')
[ "$STATUS" = "ok" ] && ok "Server is up" || fail "Server not responding"

# ─────────────────────────────────────────────────────────────────────────────
# PART A — Login without 2FA (should get access_token directly)
# ─────────────────────────────────────────────────────────────────────────────
sep "A. Login — 2FA disabled (baseline)"

RESP=$(curl -s -X POST "$BASE/admin/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")

echo "$RESP" | jq .
TWO_FA_REQUIRED=$(echo "$RESP" | jq -r '.data.two_factor_required')
ACCESS_TOKEN=$(echo "$RESP" | jq -r '.data.access_token // empty')

[ "$TWO_FA_REQUIRED" = "false" ] && ok "two_factor_required=false as expected" \
  || fail "Expected two_factor_required=false but got: $TWO_FA_REQUIRED"
[ -n "$ACCESS_TOKEN" ] && ok "access_token received" \
  || fail "No access_token in response"

# ─────────────────────────────────────────────────────────────────────────────
# PART B — Get current account profile
# ─────────────────────────────────────────────────────────────────────────────
sep "B. Get account profile"

PROFILE=$(curl -s "$BASE/admin/account" \
  -H "Authorization: Bearer $ACCESS_TOKEN")

echo "$PROFILE" | jq .
NAME=$(echo "$PROFILE" | jq -r '.data.name')
[ -n "$NAME" ] && ok "Profile returned: $NAME" || fail "Profile fetch failed"

# ─────────────────────────────────────────────────────────────────────────────
# PART C — Generate 2FA setup (get secret + QR URI)
# ─────────────────────────────────────────────────────────────────────────────
sep "C. Generate 2FA setup"

SETUP=$(curl -s "$BASE/admin/account/2fa/setup" \
  -H "Authorization: Bearer $ACCESS_TOKEN")

echo "$SETUP" | jq .
TOTP_SECRET=$(echo "$SETUP" | jq -r '.data.secret')
OTPAUTH_URL=$(echo "$SETUP" | jq -r '.data.otpauth_url')

[ -n "$TOTP_SECRET" ] && ok "TOTP secret received: $TOTP_SECRET" \
  || fail "No secret in setup response"
[ -n "$OTPAUTH_URL" ] && ok "OTPAuth URL: $OTPAUTH_URL" \
  || fail "No otpauth_url in setup response"

# ─────────────────────────────────────────────────────────────────────────────
# PART D — Enable 2FA (requires a live TOTP code)
# ─────────────────────────────────────────────────────────────────────────────
sep "D. Enable 2FA"

echo ""
echo "  Secret: $TOTP_SECRET"
echo ""
echo "  → Open your authenticator app (Google Authenticator / Authy)"
echo "    and add the account manually using the secret above."
echo "    OR use this otpauth URL:"
echo "    $OTPAUTH_URL"
echo ""
echo "  → Alternatively, generate a code from the terminal:"
echo "    pip install pyotp 2>/dev/null; python3 -c \"import pyotp; print(pyotp.TOTP('$TOTP_SECRET').now())\""
echo ""
read -rp "  Enter the 6-digit TOTP code to enable 2FA: " TOTP_CODE

ENABLE_RESP=$(curl -s -X POST "$BASE/admin/account/2fa/enable" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"secret\":\"$TOTP_SECRET\",\"code\":\"$TOTP_CODE\"}")

echo "$ENABLE_RESP" | jq .
ENABLED=$(echo "$ENABLE_RESP" | jq -r '.data.two_factor_enabled')
BACKUP_CODES=$(echo "$ENABLE_RESP" | jq -r '.data.backup_codes[]' 2>/dev/null || echo "")

[ "$ENABLED" = "true" ] && ok "2FA enabled successfully" \
  || fail "Enable 2FA failed — check the TOTP code and try again"

echo ""
echo "  Backup codes (save these):"
echo "$BACKUP_RESP" 2>/dev/null || echo "$ENABLE_RESP" | jq -r '.data.backup_codes[]' | while read -r code; do
  echo "    $code"
done

# Save first backup code for part G
FIRST_BACKUP=$(echo "$ENABLE_RESP" | jq -r '.data.backup_codes[0]')

# ─────────────────────────────────────────────────────────────────────────────
# PART E — Login with 2FA ON (should return pre_auth_token)
# ─────────────────────────────────────────────────────────────────────────────
sep "E. Login with 2FA enabled — step 1"

LOGIN2=$(curl -s -X POST "$BASE/admin/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")

echo "$LOGIN2" | jq .
TWO_FA2=$(echo "$LOGIN2" | jq -r '.data.two_factor_required')
PRE_AUTH=$(echo "$LOGIN2" | jq -r '.data.pre_auth_token // empty')

[ "$TWO_FA2" = "true" ] && ok "two_factor_required=true as expected" \
  || fail "Expected two_factor_required=true but got: $TWO_FA2"
[ -n "$PRE_AUTH" ] && ok "pre_auth_token received" \
  || fail "No pre_auth_token in response"

# ─────────────────────────────────────────────────────────────────────────────
# PART F — Complete login with TOTP code
# ─────────────────────────────────────────────────────────────────────────────
sep "F. Complete login — verify TOTP code"

echo ""
read -rp "  Enter a fresh 6-digit TOTP code: " TOTP_CODE2

VERIFY=$(curl -s -X POST "$BASE/admin/auth/2fa/verify" \
  -H "Content-Type: application/json" \
  -d "{\"pre_auth_token\":\"$PRE_AUTH\",\"code\":\"$TOTP_CODE2\"}")

echo "$VERIFY" | jq .
ACCESS2=$(echo "$VERIFY" | jq -r '.data.access_token // empty')

[ -n "$ACCESS2" ] && ok "Full access_token received after 2FA verify" \
  || fail "2FA verify failed — wrong code or expired pre_auth_token"

# ─────────────────────────────────────────────────────────────────────────────
# PART G — Complete login with backup code (simulate lost phone)
# ─────────────────────────────────────────────────────────────────────────────
sep "G. Login — use a backup code instead of TOTP"

# Need a fresh pre_auth_token (the previous one may still be valid for 5 min)
LOGIN3=$(curl -s -X POST "$BASE/admin/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")
PRE_AUTH2=$(echo "$LOGIN3" | jq -r '.data.pre_auth_token')

BACKUP_RESP=$(curl -s -X POST "$BASE/admin/auth/2fa/backup" \
  -H "Content-Type: application/json" \
  -d "{\"pre_auth_token\":\"$PRE_AUTH2\",\"backup_code\":\"$FIRST_BACKUP\"}")

echo "$BACKUP_RESP" | jq .
ACCESS3=$(echo "$BACKUP_RESP" | jq -r '.data.access_token // empty')

[ -n "$ACCESS3" ] && ok "Backup code accepted — access_token received" \
  || fail "Backup code login failed"

# Verify the backup code is now consumed (second use must fail)
sep "G2. Verify backup code cannot be reused"

REUSE=$(curl -s -X POST "$BASE/admin/auth/2fa/backup" \
  -H "Content-Type: application/json" \
  -d "{\"pre_auth_token\":\"$PRE_AUTH2\",\"backup_code\":\"$FIRST_BACKUP\"}")

ERROR_CODE=$(echo "$REUSE" | jq -r '.error.code // empty')
[ "$ERROR_CODE" = "INVALID_BACKUP_CODE" ] && ok "Reuse correctly rejected" \
  || fail "Backup code was not consumed — security issue!"

# ─────────────────────────────────────────────────────────────────────────────
# PART H — Reject wrong TOTP code
# ─────────────────────────────────────────────────────────────────────────────
sep "H. Reject wrong TOTP code"

LOGIN4=$(curl -s -X POST "$BASE/admin/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")
PRE_AUTH3=$(echo "$LOGIN4" | jq -r '.data.pre_auth_token')

WRONG=$(curl -s -X POST "$BASE/admin/auth/2fa/verify" \
  -H "Content-Type: application/json" \
  -d "{\"pre_auth_token\":\"$PRE_AUTH3\",\"code\":\"000000\"}")

echo "$WRONG" | jq .
ERR=$(echo "$WRONG" | jq -r '.error.code // empty')
[ "$ERR" = "INVALID_2FA_CODE" ] && ok "Wrong code correctly rejected" \
  || fail "Wrong code was accepted — security issue!"

# ─────────────────────────────────────────────────────────────────────────────
# PART I — pre_auth_token rejected by protected endpoints
# ─────────────────────────────────────────────────────────────────────────────
sep "I. pre_auth_token cannot access protected endpoints"

PROTECTED=$(curl -s "$BASE/admin/dashboard" \
  -H "Authorization: Bearer $PRE_AUTH3")

echo "$PROTECTED" | jq .
ERR2=$(echo "$PROTECTED" | jq -r '.error.code // empty')
[ "$ERR2" = "TOKEN_REVOKED" ] || [ "$ERR2" = "UNAUTHORIZED" ] && \
  ok "pre_auth_token correctly blocked from protected routes" || \
  fail "pre_auth_token was accepted as access_token — security issue!"

# ─────────────────────────────────────────────────────────────────────────────
# PART J — Disable 2FA
# ─────────────────────────────────────────────────────────────────────────────
sep "J. Disable 2FA"

DISABLE=$(curl -s -X POST "$BASE/admin/account/2fa/disable" \
  -H "Authorization: Bearer $ACCESS2" \
  -H "Content-Type: application/json" \
  -d "{\"password\":\"$PASSWORD\"}")

echo "$DISABLE" | jq .
DISABLED=$(echo "$DISABLE" | jq -r '.data.two_factor_enabled')
[ "$DISABLED" = "false" ] && ok "2FA disabled successfully" \
  || fail "Disable 2FA failed"

# Confirm next login goes straight to access_token (no 2FA step)
LOGIN5=$(curl -s -X POST "$BASE/admin/auth/login" \
  -H "Content-Type: application/json" \
  -d "{\"email\":\"$EMAIL\",\"password\":\"$PASSWORD\"}")
TF5=$(echo "$LOGIN5" | jq -r '.data.two_factor_required')
[ "$TF5" = "false" ] && ok "Login bypasses 2FA again after disabling" \
  || fail "2FA is still required after disabling"

# ─────────────────────────────────────────────────────────────────────────────
echo ""
echo -e "\033[32m══════════════════════════════════════════\033[0m"
echo -e "\033[32m  All 2FA tests passed.\033[0m"
echo -e "\033[32m══════════════════════════════════════════\033[0m"
