# Mobile Payment Contracts — endpoints the app expects but the backend doesn't have yet

The mobile app ships two fully-written "remote" repositories that target payment
endpoints **this backend does not implement**. Until these endpoints exist, the
app keeps using its local implementations (they work offline). Once you build
them, flipping the app to the real backend is a one-line default change per
domain (plus the field-naming fix below).

> ⚠️ **Field naming.** The mobile DTOs below are written in **camelCase**
> (`phoneNumber`, `isDefault`, `expectedAmountRwf`). The rest of this backend —
> and every working mobile `services/*` module — uses **snake_case**
> (`phone_number`, `is_default`). **Recommendation: implement these endpoints in
> snake_case to match the rest of the API, and the mobile mappers will be
> updated to match.** The shapes below show the fields; treat the casing as
> "to be reconciled to snake_case".

All responses use the standard envelope: `{ "data": ... }` for success,
`{ "error": { "code", "message" } }` for errors. Auth: `Bearer <access_token>`.

---

## 1. Payment methods (`#1`)

Mobile repo: `data/remote/repositories/RemotePaymentRepository.ts`.
The backend currently stores **no** customer payment methods (it takes a
`momo_phone` at package-purchase time instead). These endpoints add a
customer-managed list of MoMo/cash methods.

| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/payments/methods` | List the caller's payment methods |
| GET | `/api/v1/payments/methods/default` | The default method (or `null`) |
| GET | `/api/v1/payments/billing-profile` | Billing preferences summary |
| POST | `/api/v1/payments/methods` | Add a method |
| PATCH | `/api/v1/payments/methods/{methodId}` | Update a method |
| DELETE | `/api/v1/payments/methods/{methodId}` | Delete a method |
| PATCH | `/api/v1/payments/methods/{methodId}/default` | Set as default |

### PaymentMethod
```jsonc
{
  "id": "uuid",
  "provider": "mtn" | "airtel" | "cash",  // string
  "label": "MTN •••• 973",
  "phoneNumber": "+2507...",   // nullable
  "isDefault": true
}
```

### Responses
- `GET /methods` → `{ "data": { "items": PaymentMethod[] } }`
- `GET /methods/default` → `{ "data": PaymentMethod | null }`
- `GET /billing-profile` → `{ "data": BillingProfile }`
- mutations (POST/PATCH/DELETE) → `{ "data": { "items"?, "method"?, "defaultPaymentMethod"?, "billingProfile"?, "deleted"? } }`

### BillingProfile
```jsonc
{
  "defaultPaymentMethodId": "uuid | null",
  "mobileMoneyMethodIds": ["uuid"],
  "cardMethodIds": ["uuid"],
  "cashEnabled": true
}
```

### Requests
```jsonc
// POST /methods            (add)
{ "provider": "mtn", "label": "...", "phoneNumber": "+2507...", "isDefault": false, "idempotencyKey": "..." }
// PATCH /methods/{id}       (update)
{ "methodId": "uuid", "label": "...", "phoneNumber": "...", "isDefault": true, "idempotencyKey": "..." }
// DELETE /methods/{id}      (body)
{ "methodId": "uuid", "idempotencyKey": "..." }
// PATCH /methods/{id}/default
{ "methodId": "uuid", "idempotencyKey": "..." }
```
All mutating requests carry an `idempotencyKey` (dedupe double-taps/retries).

> The same repo also references `authorizePayment` / `capturePayment` /
> `refundPayment` (ride payment authorization). Those are **not** needed to wire
> the payment-methods screen — implement only if/when you add ride-level card
> auth. Shapes are in `paymentApi.ts` if you want them.

---

## 2. Package payment claims (`#3`)

Mobile repo: `data/remote/repositories/RemotePackagePaymentRepository.ts`.
This is a **claim-centric** manual-payment model (idempotency + a state machine:
created → submitted → approved/rejected/expired/cancelled). Your backend went a
simpler **purchase-centric** route (`POST /driver/packages/purchase` +
`/purchases/{id}/proof` + `/packages/history`), which the app's *actual purchase
screen already uses and works*. So you have two choices:

- **(A) Recommended — skip these endpoints.** Keep the purchase-centric flow.
  The app's status display (`useManualPaymentClaimsQuery`) can instead be
  repointed at `GET /driver/packages/history` (already implemented). This is a
  mobile-only change; no new backend work. Tell me and I'll do it.
- **(B) Implement the claim endpoints below** if you specifically want the
  richer claim lifecycle (idempotency guards, resubmit, cancel, admin review
  queue, audit log).

### Endpoints (option B)
| Method | Path | Purpose |
|--------|------|---------|
| GET | `/api/v1/package-payments/configuration` | Manual-payment config (providers, expiry, proof rules) |
| GET | `/api/v1/package-payments/manual-claims` | List the driver's claims (cursor-paginated) |
| GET | `/api/v1/package-payments/manual-claims/{claimId}` | Claim detail |
| POST | `/api/v1/package-payments/manual-claims` | Create a claim |
| POST | `/api/v1/package-payments/manual-claims/{claimId}/submit` | Submit for review |
| POST | `/api/v1/package-payments/manual-claims/{claimId}/resubmit` | Resubmit after rejection |
| POST | `/api/v1/package-payments/manual-claims/{claimId}/cancel` | Cancel |

### PackagePaymentConfiguration
```jsonc
{
  "mode": "manual" | "automatic" | "disabled",
  "manual": {
    "providers": [
      { "provider": "mtn"|"airtel", "displayName": "...", "merchantCode": "...", "ussdTemplate": "*182*...#", "enabled": true }
    ],
    "claimExpiresAfterMinutes": 120,
    "transactionReferenceRequired": true,
    "proofImageEnabled": true,
    "proofImageRequired": false
  },
  "version": "v1",
  "updatedAt": "ISO-8601"
}
```

### ManualPaymentClaim (response)
```jsonc
{
  "id": "uuid",
  "version": 1,                        // optimistic-concurrency counter
  "driverId": "uuid",
  "vehicleId": "uuid",
  "vehicleType": "moto|cab|hilux|fuso|rifani",
  "offerId": "...", "packageId": "...", "packageVersion": "...", "packageName": "...",
  "expectedAmountRwf": 2000,
  "provider": "mtn"|"airtel",
  "merchantCodeSnapshot": "...",
  "payerPhoneNumber": "+2507...",
  "transactionReference": "TXN... | null",
  "proofImageId": "uuid | null",
  "status": "created|submitted|approved|rejected|expired|cancelled",
  "createdAt": "ISO", "submittedAt": "ISO|null", "expiresAt": "ISO", "updatedAt": "ISO|null",
  "reviewedAt": "ISO|null", "reviewedBy": "uuid|null",
  "rejectionReasonCode": "string|null", "rejectionMessage": "string|null",
  "activationId": "uuid|null", "purchaseTransactionId": "uuid|null"
}
```
(Full optional fields — masking, clarification, support note — in
`data/remote/contracts/api/packagePaymentApi.ts`.)

### Requests
```jsonc
// POST manual-claims (create)
{ "driverId","vehicleId","vehicleType","offerId","packageId","packageVersion","packageName",
  "expectedAmountRwf","provider","payerPhoneNumber","transactionReference"?, "proofImageId"?, "idempotencyKey" }
// submit / resubmit / cancel
{ "claimId": "uuid", "idempotencyKey": "..." }
```

### Responses
- list → `{ "data": { "items": ManualPaymentClaim[], "nextCursor": string|null } }`
- detail → `{ "data": ManualPaymentClaim }`
- mutations → `{ "data": { "claim"?: ManualPaymentClaim, "approvalResult"?, "purchaseTransactionId"? } }`

---

## Wire-up after you implement

1. Reconcile casing (snake_case recommended) — ping me to adjust the mobile mappers.
2. Payment methods: set the payments repository factory to the remote repo.
3. Package claims (option B): set `packagePaymentRepositoryFactory` default `mode` to `'remote'`.
4. I re-run the mobile test suite + a live round-trip against these endpoints.
