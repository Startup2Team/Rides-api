# Security Architecture Guide

This document outlines the security posture, authentication protocols, and threat mitigations implemented in the Taravelis/Rides API.

---

## 1. Authentication & Token isolation

The API separates consumer/driver access from administrative panel controls:

- **User JWT**: Customer and driver authentication tokens are signed using `JWT_SECRET`. They grant access to general ride-hailing endpoints.
- **Admin JWT**: Administrative access tokens are signed using a separate `JWT_ADMIN_ACCESS_SECRET`.
- **AuthenticateAdmin Middleware**: Enforces that admin routes are only accessible via tokens signed with the admin secret, preventing privilege escalation where a driver/customer token could be presented to admin APIs.
- **Refresh Token Rotation**: Refresh tokens are rotated on each `/auth/refresh` request, invalidating old sessions.
- **Redis Session Validation**: Active sessions are tracked in Redis under `session:{jti}`. If a user is suspended, approved, or has their role modified, their sessions are immediately revoked from Redis, terminating their active logins.

---

## 2. WebSocket Single-Use Ticket Exchange

WebSocket upgrades cannot easily present HTTP headers (such as `Authorization: Bearer <token>`) in standard browser/native WebSocket clients. Passing raw access tokens in query parameters is deprecated due to URL leakage risks in logs and proxies.

Taravelis implements a **One-Time Ticket Exchange** flow:
1. The authenticated client requests a ticket via `POST /api/v1/auth/ws-ticket` using their bearer token.
2. The server generates a short-lived (60-second) signed JWT containing `token_type: "ws"` and a unique `jti`.
3. The ticket is registered in Redis under `ws-ticket:{jti}` with a 60s TTL.
4. The client initiates the WebSocket handshake: `wss://api.rides.rw/api/v1/ws/driver?ticket=<jwt>`.
5. The `Authenticate` middleware extracts the ticket, verifies the JWT signature, and checks Redis.
6. **Immediate Deletion**: The middleware immediately deletes the ticket from Redis (`GET` + `DEL`). This prevents replay attacks or connection hijacking if a URL is sniffed.

---

## 3. Secured Public Proxy Uploads

To upload driver onboarding documents (onboarding KYC) or profile images, the system uses a secured presigned proxy flow:
1. The app requests a presigned URL. The server generates a short-lived upload JWT containing the `user_id` and the specific `object_key`.
2. The upload request is sent to the proxy endpoint: `PUT /api/v1/uploads/objects/{key}?token=<jwt>`.
3. The server validates the token, matching the `user_id` and `object_key`. In production, unauthorized uploads are rejected immediately (401/403).

---

## 4. Production Startup Guards

To prevent misconfigurations in production environments, the API enforces strict startup validation:
- **TOTP Encryption Key**: If `TOTP_ENCRYPTION_KEY` is missing or matches the default development fallback key (`dev_totp_secret_fallback_key_32bytes`), the server will refuse to start and panic.
- **JWT Secrets**: Access and refresh secrets must be set and cannot match development defaults in production mode.
- **Auto-Confirm Lockout**: The dev package auto-confirm utility is strictly locked to `development` environments. In staging and production, the server crashes if auto-confirm is flagged, ensuring authoritative MTN MoMo settlement runs.

---

## 5. Structured Log Masking (PII)

Rwandan data privacy compliance requires that phone numbers (MSISDNs) are not leaked in log files. 
- **MaskMSISDN**: All structured logs (via `rs/zerolog`) mask phone numbers before writing.
- **Formatting**: Raw inputs like `+250788123456` are masked as `+2507***`.
- **Coverage**: Logging within the OTP flow, SMS send failures, and WhatsApp dispatch services uses masked values.

---

## 6. Rate Limiting Policy

- **Global Limit**: Caps IP-based requests at `GLOBAL_RATE_LIMIT_PER_MIN` (defaults to 300/min) across all non-health routes.
- **OTP Send Limit**: OTP registration SMS is capped at 5/hour per phone number.
- **OTP Verify Limit**: Bruteforce lockout restricts verification attempts to 10 per 15 minutes per phone.
- **Auth Refresh**: Token refresh is limited to `RATE_LIMIT_AUTH_REFRESH` (defaults to 20/15min).
- **Admin Login**: Admin login and 2FA verify attempts are limited to `RATE_LIMIT_ADMIN_LOGIN` (defaults to 5/5min).
