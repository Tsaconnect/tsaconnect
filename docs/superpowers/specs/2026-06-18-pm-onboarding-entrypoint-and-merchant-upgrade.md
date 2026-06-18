# PM Onboarding: Entry Point + Existing-Merchant Upgrade — Design Spec

**Date:** 2026-06-18
**Status:** Draft for independent review
**Author:** Claude (with ojimcy)

## Problem

Two gaps in how users reach Payment-Merchant (PM) onboarding:

1. **No PM-specific entry point.** The PM tab (`tsa-app/app/(dashboard)/(tabs)/(marketplace)/pm.tsx`)
   just redirects to the normal P2P marketplace filtered to PM ads (`/p2p?pm=1`). That screen
   reuses the *generic* P2P CTAs — a "create ad" button and a "Become a **merchant**" card — and
   the application form (`merchant-request.tsx`) defaults `businessType` to `general_products`
   (line 96). A user who wants to *become a PM* sees generic merchant framing and must manually
   switch the business-type picker to "P2P Merchant". There is no "Become a Payment Merchant" path.

2. **Existing merchants cannot become PMs.** `SubmitMerchantRequest`
   (`tsa-api-go/internal/handlers/merchant_request_handler.go:8`) hard-rejects any caller whose role
   is already `merchant`:
   ```go
   if user.Role == models.RoleMerchant {
       utils.ErrorResponse(c, 400, "You are already a merchant"); return
   }
   ```
   Approval of a PM request sets `role = RoleMerchant` (line 306) and provisions the PM. So a
   regular `role=user` can apply → approve → become a PM, but an **existing merchant** (e.g. a
   general-products merchant) is blocked at submit and has **no upgrade path** to PM. The
   approval/provisioning code is already role-agnostic — only the submit guard locks them out.

## Goals

- A clear "Become a Payment Merchant" entry point from the PM surface that lands on the application
  form pre-set to "P2P Merchant".
- Allow an existing merchant to apply for a PM upgrade, with a correct guard so an already-active PM
  cannot double-apply.
- No change to the approval/provisioning/money path beyond unblocking the submit gate.

## Non-Goals

- No change to PM provisioning, the NGN ledger, release-signer, or Nomba integration.
- No new role. A PM remains `role=merchant` + an active `payment_merchants` row.
- No bulk migration of existing merchants. Upgrade is opt-in, per user.

## Design

### Part A — App: PM entry point + pre-selected business type

**A1. Form accepts a `type` query param.** `merchant-request.tsx` reads
`useLocalSearchParams<{ type?: string }>()`. When `type === 'p2p_merchant'`, initialize
`businessType` to `'p2p_merchant'` instead of `'general_products'`, so the form opens in PM mode
(the existing `isPm` logic at line 139 then reveals the "Payment Merchant setup" section).

**A2. Lock the picker when entered via the PM path.** The picker is a row of `Chip`s
(`merchant-request.tsx:383-392`) with no `disabled` prop. When `type === 'p2p_merchant'`, replace
the chip row with a single **static locked label / selected-only "P2P Merchant" chip** (no
`onPress`), so the type can't be changed (the user explicitly chose the PM path). When the form is
reached without the param, the chip row behaves exactly as today (all options selectable, default
`general_products`). Note the `rejected` → "Apply Again" reset handler
(`merchant-request.tsx:336-354`) does **not** reset `businessType`, so a rejected PM applicant stays
in PM mode on retry — which is the intended behavior here; cover it in tests.

**A3. PM CTA on the `pm=1` marketplace view.** In `p2p/index.tsx`, when the marketplace is in PM
mode (`pm` param set) and the viewer is **not already an active PM**, show a "Become a Payment
Merchant" `AccentCard` that routes to `/merchants/merchant-request?type=p2p_merchant`. This replaces
the generic "Become a merchant" card *on the PM view only*; the normal (`pm` unset) marketplace
keeps its existing generic card unchanged.

**A4. "Become a Payment Merchant" entry from the merchant dashboard.** On
`tsa-app/app/merchants/dashboard`, surface an "Upgrade to Payment Merchant" entry (visible to
merchants who are not already an active PM) routing to the same
`/merchants/merchant-request?type=p2p_merchant`. This is the path B unblocks for existing merchants.

**Is-active-PM signal (A3/A4 gating).** Review confirmed **no** user-facing PM-status signal exists
today — the only PM reads are the admin group (`routes.go:199-208`, behind `adminAuth +
RequireP2PAdmin`); `useUserRole` knows role only. So add a minimal authenticated read
`GET /p2p/pm/me` → `{ isPm: bool, status: string }` backed by `PMService.GetActiveByUserID`, plus a
thin client hook. `isPm` is true **only when** `status == active` (matches the Part-B guard exactly,
so UI and server never disagree). **Dormant-safe:** if the PM/Nomba stack is unconfigured the route
returns `{ isPm: false }` (HTTP 200), **not** 503 — so the CTA simply shows for everyone, which is
harmless (the server submit-guard is authoritative). Gate A3/A4 on `!isPm`. Showing/hiding the CTA
has **no** security weight; Part B is the authoritative gate.

### Part B — Backend: allow existing-merchant → PM upgrade

Restructure the head of `SubmitMerchantRequest` so the role check knows the requested business
type. Bind and validate `input` **first**, then apply guards:

```
bind input
validate business type is in ValidBusinessTypes

if input.BusinessType == p2p_merchant:
    // existing merchants MAY upgrade; already-active PMs may NOT re-apply
    if active PM exists for user (PMService.GetActiveByUserID != ErrPMNotFound):
        400 "You are already a payment merchant"
    // (role==merchant is allowed to continue here — the upgrade path)
else:
    // unchanged: a merchant cannot re-apply as a general merchant
    if user.Role == RoleMerchant:
        400 "You are already a merchant"

... existing PM-field validation (settlement + maker wallet) ...

// TYPE-SCOPED duplicate-request check (see Critical-1 below):
if input.BusinessType == p2p_merchant:
    // only a prior PENDING/APPROVED *PM* request blocks a PM submit
    existing := first MerchantRequest WHERE user_id=? AND business_type='p2p_merchant'
                                            AND status IN (pending, approved)
else:
    // unchanged: any prior pending/approved request blocks a non-PM submit
    existing := first MerchantRequest WHERE user_id=? AND status IN (pending, approved)
if existing found: 400 "You already have a {pending|approved} merchant request"
```

**Critical-1 — the duplicate check MUST be type-scoped.** This is the decisive fix surfaced by
review. An existing general-products merchant earned `role=merchant` from a request that was
*approved*, and approval only flips that row's status to `approved`
(`merchant_request_handler.go:296-301`) — it is never deleted. The current duplicate check
(`WHERE user_id=? AND status IN (pending,approved)`, `merchant_request_handler.go:114-125`) is
**not** business-type-scoped, so a PM upgrade submit would match that stale approved general row and
return 400 before reaching the PM path — the upgrade would be dead on arrival for its entire target
population. Scoping the check to `business_type='p2p_merchant'` for PM submits fixes this; the non-PM
branch keeps the original unscoped check (a general merchant still can't re-apply as general).

**Critical-2 / coupled — closing the second-PM-row hole.** `GetActiveByUserID` only blocks on an
*active* PM, so a prior **failed-provisioning** PM (non-active `payment_merchants` row + a stuck
`approved` PM request, `merchant_request_handler.go:340-343`) would slip past the active-PM guard.
The type-scoped duplicate check above closes this: the stuck `approved` **PM** request now blocks
the resubmit. The two fixes land together. (The approve-time `isPMRetry` lookup
`Where("user_id = ?").First(&pmRow)` at line 280 is status-agnostic and finds the first PM row —
the resume path — which is correct *only* when at most one PM request can be outstanding; the
type-scoped duplicate check is what guarantees that.)

**Guarantees / invariants:**
- Non-PM business types: same status codes for well-formed bodies; merchant still blocked. **Caveat
  (ordering):** moving the `role==merchant` check after `ShouldBindJSON` means a `role=merchant`
  caller sending *malformed* JSON now gets "Invalid request body" instead of "already a merchant"
  (today the role check fires first). No existing test asserts this ordering; the change is benign
  and arguably more correct, but it is a behavior change — documented here, not "byte-for-byte".
- Already-active PM: blocked with a distinct message (no duplicate PM rows).
- Duplicate pending/approved **PM** request: blocked by the type-scoped lookup (also shields the
  failed-provisioning second-PM case).
- PM-field validation (settlement bank/account/name + non-zero maker wallet) unchanged, still
  required for every `p2p_merchant` submit.
- Approval path unchanged: for an upgrading merchant with no prior PM row → `isPMRetry=false` →
  normal approve → role stays `merchant` + PM provisioned. True-admin gate at approval
  (`merchant_request_handler.go:332-335`) is intact and authoritative — the submit gate was never a
  security control (no privilege escalation; confirmed by review).

**Error-message distinctness:** active-PM rejection uses "You are already a payment merchant"
(distinct from the general "You are already a merchant") so the client can message correctly.

## Edge cases

- **General-products merchant upgrading to PM:** allowed; keeps general-merchant capability and
  gains PM (role already `merchant`; a `payment_merchants` row is added at approval). Ads auto-tag as
  PM only when maker-wallet-gated (existing behavior).
- **Active PM hits the form again:** server returns 400 "already a payment merchant"; the UI should
  not have offered the CTA (A3/A4 gated on `!isPm`), but the server is the authoritative gate.
- **Merchant with a pending PM request re-submits:** blocked by the existing duplicate check.
- **`role=user` applying as PM:** unchanged from today (works).
- **Form reached without `type` param:** unchanged — picker fully selectable, default
  `general_products`.

## Testing

- **Backend (handler tests):**
  1. **`role=merchant` WITH a pre-existing *approved general_products* `MerchantRequest` row** +
     valid PM fields → **201**. This is the headline case and it **must seed the stale approved
     general row** — without it the test passes against the Critical-1 bug and provides false
     assurance.
  2. `role=merchant` + `p2p_merchant` but an active PM already exists → 400 "already a payment
     merchant".
  3. **Failed-provisioning resubmit:** merchant with a non-active `payment_merchants` row + a stuck
     `approved` p2p_merchant request re-submits → **400** (blocked by the type-scoped duplicate
     check); assert exactly **one** `payment_merchants` row exists (Critical-2).
  4. `role=merchant` + non-PM business type → 400 "already a merchant" (unchanged, well-formed body).
  5. `role=user` + `p2p_merchant` → 201 (unchanged).
  6. `p2p_merchant` missing settlement/maker fields → 400 (unchanged).
  7. `GET /p2p/pm/me`: active PM → `{isPm:true}`; non-PM → `{isPm:false}`; PM stack unconfigured →
     `{isPm:false}` 200 (dormant-safe, not 503).

  Note pre-existing `internal/handlers` SQLite-DDL failures are unrelated.
- **App (jest):** (1) form initializes `businessType='p2p_merchant'` when `type` param present and
  renders the locked single-chip/label (no other types selectable); (2) form unchanged (default
  `general_products` + all chips selectable) without the param; (3) PM CTA renders on `pm=1` view for
  a non-PM and routes with `?type=p2p_merchant`; (4) CTA hidden for an active PM; (5) rejected →
  "Apply Again" keeps PM mode (does not reset `businessType`).

## Rollout

Dormant-safe: if the PM/Nomba stack is unconfigured, approval still 503s as today — this change only
affects who may *submit* and how they *reach* the form. No migration. No new env.
