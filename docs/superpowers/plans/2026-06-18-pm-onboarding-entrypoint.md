# PM Onboarding Entry Point + Existing-Merchant Upgrade — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task.

**Goal:** Let users reach PM onboarding via a "Become a Payment Merchant" entry point, and let existing merchants upgrade to PM — without changing the approval/provisioning/money path.

**Architecture:** Backend restructures `SubmitMerchantRequest`'s guards (bind-first, PM carve-out, active-PM guard, type-scoped duplicate check) and adds a dormant-safe `GET /p2p/pm/me`. App reads a `?type=p2p_merchant` param to open the form in locked PM mode, and shows PM CTAs gated on `/pm/me`.

**Tech Stack:** Go 1.24 + Gin + GORM (backend); React Native + Expo Router (app).

**Spec:** `docs/superpowers/specs/2026-06-18-pm-onboarding-entrypoint-and-merchant-upgrade.md` (read it; it carries the Critical-1/Critical-2 reasoning verbatim).

## Global Constraints

- A PM is `role=merchant` + an active `payment_merchants` row. **No new role.**
- The submit gate is UX/dedup only — **not** a security control. The authoritative gate is the
  true-admin check at approval (`merchant_request_handler.go:332-335`), which must stay intact.
- Dormant-by-default: unconfigured PM/Nomba stack must not 503 the new read route; it returns
  `{isPm:false}` 200. Server still boots without PM creds.
- `isPm` is true **only when** `status == active` — UI signal and server guard must agree.
- Distinct error messages: "You are already a payment merchant" (active PM) vs "You are already a
  merchant" (non-PM re-apply).

---

### Task 1: Backend — `SubmitMerchantRequest` PM-upgrade carve-out

**Files:**
- Modify: `tsa-api-go/internal/handlers/merchant_request_handler.go` (`SubmitMerchantRequest`, ~lines 1-130)
- Test: `tsa-api-go/internal/handlers/merchant_request_handler_test.go`

**Interfaces:**
- Consumes: `PMService.GetActiveByUserID(ctx, userID) (*models.PaymentMerchant, error)` returning
  `services.ErrPMNotFound` when no *active* PM exists; `models.BusinessTypeP2PMerchant`;
  `models.MerchantRequestStatusPending/Approved`.
- Produces: no new exported symbols.

**Steps:**
- [ ] **Step 1: Failing tests.** Add to `merchant_request_handler_test.go`:
  - `TestSubmitMerchantRequest_MerchantUpgradeToPM`: seed a user with `role=merchant` **and an
    existing `MerchantRequest{business_type:general_products, status:approved}`** row; POST a valid
    `p2p_merchant` body (settlement fields + non-zero maker wallet) → expect **201**.
  - `TestSubmitMerchantRequest_AlreadyActivePM`: seed `role=merchant` + an **active**
    `payment_merchants` row for the user; POST valid `p2p_merchant` body → **400**, body contains
    "already a payment merchant".
  - `TestSubmitMerchantRequest_FailedProvisioningResubmitBlocked`: seed `role=merchant` + a
    **non-active** `payment_merchants` row + a stuck `MerchantRequest{business_type:p2p_merchant,
    status:approved}`; POST valid `p2p_merchant` body → **400**; assert exactly **one**
    `payment_merchants` row for the user afterward.
  - Keep/confirm existing `TestSubmitMerchantRequest_AlreadyMerchant` (valid non-PM body, `role=merchant`) → 400 "already a merchant".
- [ ] **Step 2: Run, verify fail.** `cd tsa-api-go && go test ./internal/handlers -run TestSubmitMerchantRequest -v` → new tests fail (current code 400s the upgrade via the unscoped duplicate check / pre-bind role block).
- [ ] **Step 3: Restructure the guard.** Reorder so binding happens before the role check; implement per spec §"Part B":
  - Bind input + validate `BusinessType` in `ValidBusinessTypes` first.
  - If `BusinessType == p2p_merchant`: if `GetActiveByUserID` returns a PM (err == nil) → 400 "You
    are already a payment merchant". Do **not** block on `role==merchant` here.
  - Else (non-PM): keep `if user.Role == RoleMerchant → 400 "You are already a merchant"`.
  - Keep PM-field validation (settlement + maker wallet) exactly as-is.
  - **Type-scope the duplicate check:** for `p2p_merchant`, query `existing` with an added
    `business_type = ?` = `p2p_merchant`; for non-PM keep the original unscoped query. Preserve the
    existing pending-vs-approved message branch.
- [ ] **Step 4: Run, verify pass.** `go test ./internal/handlers -run TestSubmitMerchantRequest -v` → all pass. (Pre-existing unrelated SQLite-DDL failures in the package are acceptable; the `-run` filter avoids them.)
- [ ] **Step 5: Commit.** `feat(pm): allow existing-merchant upgrade to PM (type-scoped guards)`

---

### Task 2: Backend — dormant-safe `GET /p2p/pm/me`

**Files:**
- Modify: `tsa-api-go/internal/handlers/pm_handler.go` (add `GetMyPMStatus`)
- Modify: `tsa-api-go/internal/routes/routes.go` (register the route in the authenticated user group)
- Test: `tsa-api-go/internal/handlers/pm_handler_test.go` (create if absent)

**Interfaces:**
- Consumes: `PMService.GetActiveByUserID`; `getUserFromContext(c)`.
- Produces: `GET /p2p/pm/me` → `200 {"isPm": bool, "status": string}`.

**Steps:**
- [ ] **Step 1: Failing test.** `TestGetMyPMStatus`: (a) active PM → `{isPm:true,status:"active"}`;
  (b) no PM → `{isPm:false,status:""}`; (c) handler constructed with a **nil PMService** (simulates
  dormant/unconfigured) → `200 {isPm:false}`, never 503/panic.
- [ ] **Step 2: Run, verify fail.** `go test ./internal/handlers -run TestGetMyPMStatus -v`.
- [ ] **Step 3: Implement `GetMyPMStatus`.** Auth required; if `h.PMService == nil` → return
  `{isPm:false}`. Else call `GetActiveByUserID`; on `ErrPMNotFound` → `{isPm:false,status:""}`; on a
  found active PM → `{isPm:true,status:"active"}`; on other errors → log + `{isPm:false}` (fail open
  to UX-safe, never 503). Register `userGroup.GET("/p2p/pm/me", pmHandler.GetMyPMStatus)` behind the
  existing auth middleware (NOT the admin group).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit.** `feat(pm): add dormant-safe GET /p2p/pm/me user PM-status read`

---

### Task 3: App — form reads `?type=p2p_merchant` + locked picker

**Files:**
- Modify: `tsa-app/app/merchants/merchant-request.tsx`
- Test: `tsa-app/app/merchants/__tests__/merchant-request-pm.test.ts` (extend)

**Interfaces:**
- Consumes: `useLocalSearchParams<{ type?: string }>()` from `expo-router`.
- Produces: no new exports (screen behavior only).

**Steps:**
- [ ] **Step 1: Failing tests.** Extend the test file: (a) with `type='p2p_merchant'` param,
  `businessType` initializes to `'p2p_merchant'` and the locked single-chip/label renders (other
  type chips absent/non-pressable); (b) without the param, default `general_products` + all chips
  present (unchanged); (c) rejected → "Apply Again" does not reset `businessType` away from
  `p2p_merchant`. (Use the existing test's render/param-mock pattern.)
- [ ] **Step 2: Run, verify fail.** `cd tsa-app && npx jest app/merchants/__tests__/merchant-request-pm.test.ts`.
- [ ] **Step 3: Implement.** Import `useLocalSearchParams`; read `type`; initialize
  `businessType` state from `type === 'p2p_merchant' ? 'p2p_merchant' : 'general_products'`. When
  `type === 'p2p_merchant'`, render a single static/selected "P2P Merchant" chip (no `onPress`)
  instead of the selectable chip row (`merchant-request.tsx:383-392`). Leave the `rejected` reset
  handler (~336-354) untouched (it already omits `businessType`).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit.** `feat(pm app): merchant-request opens in locked PM mode via ?type=p2p_merchant`

---

### Task 4: App — PM CTAs gated on `/pm/me`

**Files:**
- Modify: `tsa-app/components/services/api.ts` (or `services/p2p.ts`) — add `getMyPmStatus()`
- Create: `tsa-app/hooks/useIsPaymentMerchant.ts` (thin hook over the read)
- Modify: `tsa-app/app/p2p/index.tsx` — PM CTA on the `pm=1` view
- Modify: `tsa-app/app/merchants/dashboard/index.tsx` — "Upgrade to Payment Merchant" entry
- Test: `tsa-app/app/p2p/__tests__/` (extend or create)

**Interfaces:**
- Consumes: `GET /p2p/pm/me` → `{isPm, status}`; existing `pm` search param in `p2p/index.tsx:68`.
- Produces: `useIsPaymentMerchant(): { isPm: boolean, loading: boolean }`.

**Steps:**
- [ ] **Step 1: Failing tests.** (a) On the `pm=1` view with `useIsPaymentMerchant → {isPm:false}`,
  a "Become a Payment Merchant" CTA renders and `onPress` routes to
  `/merchants/merchant-request?type=p2p_merchant`; (b) with `{isPm:true}`, the CTA is absent.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement.** Add `getMyPmStatus()` to the api service (GET `/p2p/pm/me`, returns
  `{isPm,status}`, defaults to `{isPm:false}` on any error). Add `useIsPaymentMerchant` hook.
  In `p2p/index.tsx`, when `pm` param is set and `!isPm`, render a "Become a Payment Merchant"
  `AccentCard` (mirror the existing "Want to sell?" card at ~245) routing to
  `/merchants/merchant-request?type=p2p_merchant`; the non-PM (`pm` unset) view keeps its existing
  generic card untouched. In `merchants/dashboard/index.tsx`, add an "Upgrade to Payment Merchant"
  entry visible when `!isPm`, routing to the same target (net-new UI — no existing merchant-request
  link there today).
- [ ] **Step 4: Run, verify pass.**
- [ ] **Step 5: Commit.** `feat(pm app): Become-a-PM CTA on PM marketplace + dashboard upgrade entry`

---

## Notes for the executor

- Tasks 1→2 are backend (money/role path) — review them most strictly. Tasks 3→4 are app/UX.
- Task 4 depends on Task 2's route shape and Task 3's `?type` param contract.
- The pre-existing `internal/handlers` SQLite-DDL test failures (`persona_inquiry_id`,
  `fee_distributions`) are unrelated — use `-run` filters; do not "fix" them here.
