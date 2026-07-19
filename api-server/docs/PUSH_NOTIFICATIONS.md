# Push Notifications (FCM) — Setup & Architecture

Real push notifications for **both Android and iPhone**, delivered through
Firebase Cloud Messaging (FCM) via the Firebase Admin SDK on the backend.
Notifications reach **both customers and drivers**.

This doc has two parts:

1. **Owner setup** — the exact Firebase-console / secret steps someone with
   access to the Firebase project must perform (some are already done).
2. **Architecture** — how delivery is wired, for developers.

---

## 1. Owner setup steps

Firebase project: **`rides-91f49`** (project number `982790117122`).

### 1.1 Backend service account (Android + iOS delivery) — ✅ DONE

The backend sends via the Firebase Admin SDK, which authenticates with a
**service-account JSON**.

- Firebase console → Project settings → **Service accounts** → *Generate new
  private key* → download the JSON.
- Place it where the api container can read it and point the env var at it:
  - `FIREBASE_SERVICE_ACCOUNT_PATH=/app/firebase-service-account.json`
- The file is a **secret** — it is gitignored (`firebase-service-account.json`)
  and must **never** be committed or printed.

Status: the real service account for `rides-91f49` is mounted in the running
api container and the FCM client initializes clean. When the path is unset or
the file is invalid, the service logs a warning and falls back to a no-op
client (persist-only, no push) — the app keeps working, just without delivery.

### 1.2 Android app config — ✅ DONE

- Firebase console → add / confirm the **Android app** with package
  `rw.rides.app`.
- Download `google-services.json` → it lives at
  `artifacts/mobile/google-services.json` and is referenced from `app.json`
  (`android.googleServicesFile`). The committed file is the real one for
  `rides-91f49`.
- Android's native token from `getDevicePushTokenAsync()` is already a valid
  **FCM registration token**, so Android works as soon as the backend service
  account is in place (1.1).

### 1.3 iOS app config — ⛔ OWNER ACTION REQUIRED

iOS needs three things the owner must provide (console + Apple Developer):

1. **Create the iOS app in Firebase** for bundle id `rw.rides.app`
   (Firebase console → Add app → iOS). Download its
   **`GoogleService-Info.plist`** and overwrite the placeholder at
   `artifacts/mobile/GoogleService-Info.plist` (already referenced from
   `app.json` → `ios.googleServicesFile`). The placeholder has the correct
   `PROJECT_ID` / `GCM_SENDER_ID` / `STORAGE_BUCKET`; you must replace
   `GOOGLE_APP_ID` and `API_KEY` with the real iOS-app values.

2. **APNs auth key** (Apple → Firebase). Apple Developer portal →
   Certificates, Identifiers & Profiles → **Keys** → create a key with
   *Apple Push Notifications service (APNs)* enabled → download the `.p8`.
   Then Firebase console → Project settings → **Cloud Messaging** → *Apple app
   configuration* → upload the `.p8` with its **Key ID** and your **Team ID**.
   This is what lets FCM bridge to APNs for iOS delivery.

3. **Enable Push Notifications capability** on the App ID / provisioning
   (handled by EAS build; the entitlement `aps-environment` is declared in
   `app.json` → `ios.entitlements`).

> **Why iOS is different:** on iOS `getDevicePushTokenAsync()` returns an
> **APNs** token, but `messaging.Send` on the backend targets an **FCM** token.
> The app must therefore obtain a true FCM token on-device via Firebase (see
> §2.4). Until the plist + APNs key above are in place, iOS devices fall back to
> registering the APNs token, which the Admin SDK cannot deliver to.

### 1.4 (Optional) Web push

There is an FCM **Web Push VAPID key**
(`BLqYWsVlpobl…`). It is for **web** push only (admin web / a web build) and is
**not** used by the native iOS/Android app. Do not wire it into the native app.

### Environment variables summary

| Var | Where | Value |
|---|---|---|
| `FIREBASE_SERVICE_ACCOUNT_PATH` | backend (api container) | path to the service-account JSON, e.g. `/app/firebase-service-account.json` |

No new mobile env vars — the client reads `google-services.json` /
`GoogleService-Info.plist` at build time.

---

## 2. Architecture

### 2.1 Token registration (mobile → backend)

- `services/fcmToken.ts` obtains the right native token per platform:
  - **Android** → native FCM token from `expo-notifications`.
  - **iOS** → FCM token via `@react-native-firebase/messaging` when that native
    module is present in the build (it bridges FCM→APNs); otherwise falls back
    to the APNs token. The dependency is loaded through an **optional** runtime
    require so the app still bundles/runs without it.
- `services/pushRegistration.ts` requests permission (after a positive moment —
  on login / session restore, not cold launch), gets the token, and registers
  it via `POST /api/v1/users/me/device-token { token, platform }`.
  Logout calls `DELETE /api/v1/users/me/device-token`.
- The backend stores tokens **multi-device** in `device_tokens` and mirrors the
  latest into `users.fcm_token` (legacy readers).

> To finish iOS on-device FCM: `pnpm add @react-native-firebase/app
> @react-native-firebase/messaging`, add the `@react-native-firebase/app`
> config plugin to `app.json`, and run a native build. `fcmToken.ts` picks the
> module up automatically — no code change needed.

### 2.2 Centralized delivery (the "create → push" path)

`internal/notification/service.go` → **`SendToAllDevices(ctx, userID, title,
body, nType, data)`** is the single centralized entry point:

1. **Persists** the notification (in-app history + unread badge).
2. Loads **every** device token for the user and **pushes** via FCM.
3. **Prunes dead tokens** (FCM `unregistered`/`not-registered`) as a side
   effect.

Any caller that creates a notification through this method gets an FCM push for
free. `fcm.go` sets Android `priority: high` and APNS `apns-priority: 10`.

### 2.3 Trigger matrix (event → audience → title / body)

| Event | Audience | Title / Body | Where |
|---|---|---|---|
| New ride request (offered) | Driver | "New ride request" / "A rider is Nm away…" | `matching/engine.go` `offerToDriver` |
| Driver matched / accepted | Customer | "Driver found" / "A driver accepted your ride…" | `matching/engine.go` `notifyCustomerDriverMatched` |
| Fare confirmed | Customer + Driver | "Ride confirmed" / "Fare agreed: RWF N…" | `negotiation/service.go` Accept + LockManualFare → `ride.NotifyFareConfirmed` |
| Negotiation counter-offer | The other party | "New fare offer" / "New fare offer: RWF N" | `negotiation/service.go` Propose → `ride.NotifyNegotiationOffer` |
| Driver en-route | Customer | "Driver on the way" / "…heading to the pickup point." | `ride/service.go` `SetEnRoute`, `SetDriverArrived` |
| Driver arrived | Customer | "Driver arrived" / "Your driver is at the pickup point." | `ride/service.go` `MarkDriverArrived`, `SetDriverArrived` |
| Ride started | Customer | "Ride started" / "Your trip has started…" | `ride/service.go` `StartRide` |
| Ride completed | Customer | "Ride completed" / "…Fare: N RWF" | `ride/service.go` `CompleteRide` (pre-existing) |
| Ride cancelled (by customer) | Driver | "Ride cancelled" / "The customer cancelled…" | `ride/service.go` `CancelRide` (pre-existing) |
| Ride cancelled (by driver / no-show) | Customer | "Ride cancelled" / … | `ride/service.go` `DriverCancelRide`, `CancelAfterPickupExpiry` (pre-existing) |
| Application received | Driver | "Application received" / "We've received your driver application…" | `driver/service.go` `Apply` |
| Application approved | Driver | "You're approved!" / "…go online and start accepting rides." | `admin/drivers.go` `ApproveDriver` |
| Application rejected | Driver | "Application update" / "…was not approved. Reason: …" | `admin/drivers.go` `RejectDriver` |
| Credits / entitlement low | Driver | "Buy a package to keep riding" / "You're out of ride credits…" | `driver/service.go` `SetAvailability` (go-online gate) |
| Package approved / rejected | Driver | (owned by the package-payments domain) | pushed automatically because that domain calls the notification service |
| Document expiry (license/insurance/authorization) | Driver | day-mark warnings | `driver/expiry_notifier.go` (pre-existing) |

### 2.4 Receipt on device (mobile)

`services/usePushNotifications.ts` (mounted in `app/_layout.tsx`) wires:

- **Foreground receipt** → refresh the in-app feed + unread badge.
- **Tap** (background / cold start) → refresh, then deep-link by `data.type`
  (ride-flow types → `/ride`, everything else → `/notifications`).

Android also registers a high-importance `default` notification channel with a
brand accent color (`configurePushNotifications`).

---

## 3. Verifying without a physical device

- **Token round-trip:** register a test user via the API, `POST
  /users/me/device-token`, then confirm it in `device_tokens`.
- **Send path fires:** create any notification (e.g. drive a ride event, or
  register a bogus token then trigger a notification) and watch the api logs —
  a real send is attempted; an invalid token returns FCM `unregistered` and the
  token is pruned from `device_tokens` + `users.fcm_token`.
- **In-app persistence:** `GET /users/me/notifications` shows the record even
  when push delivery is off.
- **Full delivery** requires a real device with the app installed (Android:
  works now; iOS: after §1.3).
