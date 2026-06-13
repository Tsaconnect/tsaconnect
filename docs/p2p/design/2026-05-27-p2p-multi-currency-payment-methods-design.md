# P2P Multi-Currency Browse + Payment-Method Filter — Design

**Date:** 2026-05-27
**Scope:** Make the P2P **Buy** experience work for any fiat currency (not NGN-only), defaulting to the user's country currency, with an "All Payment Methods" filter and payment chips driven by a normalized provider model. De-NGN the copy/defaults and keep a clean empty state for currencies with no traders.

---

## Overview

Today the P2P browse screen is hard-wired to a single market: [app/p2p/index.tsx:43](tsa-dev/tsa-app/app/p2p/index.tsx#L43) lists only `side: 'sell'`, `fiatCurrency: 'NGN'`, the header reads "Buy {token} for NGN", and the price/limit blocks render `NGN`/`₦` literally ([index.tsx:118](tsa-dev/tsa-app/app/p2p/index.tsx#L118), [:220](tsa-dev/tsa-app/app/p2p/index.tsx#L220), [:244](tsa-dev/tsa-app/app/p2p/index.tsx#L244)). There is no currency selector and no payment-method filter, even though the listing endpoint and `CurrencyContext` already support multiple currencies.

This spec makes the Buy side multi-currency and adds payment-method filtering:

1. A **P2P-scoped market currency selector** in the header, defaulting from the existing geolocation logic.
2. A normalized **`provider`** field on payment methods (e.g. `bank_transfer`, `opay`, `paypal`, `revolut`, `interac`) so the "All Payment Methods" filter and ad chips work and can represent international methods.
3. A privacy-safe **payment summary** on listed ads so cards show "Bank Transfer · OPay" chips without broadcasting account numbers.
4. **De-NGN cleanup** of all copy and defaults.
5. A clean **empty state** when a currency has no ads.
6. **Currency-aware create-ad**, with the float option disabled for currencies that have no market anchor.

### Out of scope (separate specs)
- **User-sells "Sell" side.** The trade engine is one-directional: every ad is a *seller* selling crypto from their escrow pool, and the trade flow is open → mark-paid → seller-releases ([services/p2p.ts:504-544](tsa-dev/tsa-app/services/p2p.ts#L504-L544)). There is no machinery for a user to deposit crypto and sell it to a trader. The Bybit-style "Sell" tab is a later spec.
- **Multi-chain deposits / asset-by-chain selection** — deferred to the "incremental multi-chain" spec (Spec 2).
- **Per-country payment-method catalog** for empty currencies — explicitly declined; empty currencies show the "Post an ad" CTA only.

---

## Architecture

```
P2P browse (Buy)                              Add payment method
   │                                              │
   │ useP2pCurrency() → CAD (persisted,           │ GET /p2p/payment-providers  → catalog
   │   default from CurrencyContext geo)          │ pick provider → POST /p2p/payment-methods
   ▼                                              │   { provider, type, country, currency, ... }
adsApi.list({ side:'sell',                        ▼
   token, fiatCurrency:'CAD',                   provider stored on row
   provider?:'interac' })
   │
   ▼
GET /p2p/ads  ──► each ad decorated with
   paymentMethodSummary: [{provider,label}]   ◄── join ad.paymentMethodIds → methods
   (NO account numbers)                            → dedupe provider labels
   │
   ▼
Frontend:
   • currency pill (header)
   • payment filter = distinct providers across loaded ads
   • ad cards render chips from paymentMethodSummary
   • empty currency → "No <token> sellers for CAD yet — Post an ad"
```

The market currency is **P2P-scoped** (its own persisted selection), deliberately decoupled from the app-wide display currency in `CurrencyContext` so switching the trading market to CAD does not flip the user's portfolio display currency. It *defaults* from `CurrencyContext`'s geolocation-derived currency on first use.

---

## Data model

The backend uses GORM `AutoMigrate` ([config/database.go](tsa-dev/tsa-api-go/internal/config/database.go)), which emits column additions from struct tags automatically. Adding `provider` requires only the struct field/tag plus a one-time backfill via `DB.Exec`.

### `P2PPaymentMethod` — new column

| Column | Type | Default | Nullable |
|---|---|---|---|
| `provider` | `varchar(32)` | `'bank_transfer'` | NOT NULL |

`type` (`bank | mobile_money`) is widened to include `wallet` for PayPal/Revolut/Wise/Skrill-style methods. `type` becomes a coarse category derived from the provider; `provider` is the precise, filterable identifier.

### Provider catalog (backend source of truth)

A Go map in a new `internal/services/p2p_payment_providers.go` (slug → `{Label, Type, IdentifierLabel}`), so frontend and backend agree on labels. `IdentifierLabel` drives the account-field label in the add-method UI ("Account number", "Email", "Tag/Phone"). Storage stays in the existing `account_number` / `account_name` columns regardless of provider.

Initial catalog:

| Slug | Label | Type | Identifier label |
|---|---|---|---|
| `bank_transfer` | Bank Transfer | bank | Account number |
| `opay` | OPay | mobile_money | Account/phone |
| `palmpay` | PalmPay | mobile_money | Account/phone |
| `kuda` | Kuda | bank | Account number |
| `interac` | Interac e-Transfer | bank | Email |
| `paypal` | PayPal | wallet | Email |
| `revolut` | Revolut | wallet | Tag/phone |
| `wise` | Wise | wallet | Email |
| `skrill` | Skrill | wallet | Email |

The catalog is extensible; adding a provider is a one-line map entry. There is **no per-currency gating** of providers in this spec — a trader may attach any provider to a method in any currency.

### Backfill (one-time, `DB.Exec` after AutoMigrate)
- `type = 'bank'`  → `provider = 'bank_transfer'`
- `type = 'mobile_money'` → match `lower(bank_name)` against known mobile slugs (`opay`, `palmpay`, `kuda`); else `'bank_transfer'`.

### `P2PAd` — list-response decoration (not persisted)
The list endpoint adds, per ad, a privacy-safe summary derived from `payment_method_ids`:

```
paymentMethodSummary: [{ provider: "bank_transfer", label: "Bank Transfer" }, ...]
```

Account numbers/names remain exposed only by the ad-detail endpoint ([services/p2p.ts:63-66](tsa-dev/tsa-app/services/p2p.ts#L63-L66)), unchanged.

---

## Backend changes (tsa-api-go)

1. **`models/p2p_payment_method.go`** — add `Provider string` with GORM tag (`default:'bank_transfer';not null`); widen `type` enum handling to allow `wallet`.
2. **`services/p2p_payment_providers.go`** (new) — provider catalog map + lookup helpers (`ProviderLabel(slug)`, `ProviderType(slug)`, `AllProviders()`).
3. **`config/database.go`** — backfill `provider` for existing rows after AutoMigrate (idempotent `UPDATE ... WHERE provider IS NULL OR provider = ''`).
4. **Payment-method create handler** — accept + validate `provider` against the catalog; derive/persist `type` from the provider.
5. **`GET /p2p/payment-providers`** (new) — returns the full catalog for the create-ad picker.
6. **Ad-list handler** — (a) add optional `provider` query filter (join `payment_method_ids` → methods → match provider); (b) decorate each ad with `paymentMethodSummary` (deduped provider+label, no account data); (c) remove any NGN-specific assumptions (filtering is purely by the `fiatCurrency` query param, already supported).
7. **Float-anchor currencies** — expose which currencies have a market anchor (Bybit). Add a `floatAnchor bool` flag per currency in the `/api/p2p/rates` response (`p2p_service.go` already knows which currencies it can anchor). Create-ad uses this to gate the float option.

No contract changes. No changes to the trade/escrow flow.

---

## Frontend changes (tsa-app)

1. **`hooks/useP2pCurrency.ts`** (new) — returns `{ currency, setCurrency }`. Persists slug to AsyncStorage key `p2p_market_currency`; on first run defaults from `CurrencyContext` (the geolocation-derived currency). Resolves the full `CurrencyConfig` from `useCurrency().supportedCurrencies`.
2. **`app/p2p/index.tsx`** —
   - Add a currency pill to the header (top-right), opening the existing `CurrencySelector` modal via its `onChange` callback (no global currency mutation — wired to `useP2pCurrency`).
   - Pass `fiatCurrency` from `useP2pCurrency` to `adsApi.list`.
   - Add an "All Payment Methods" filter control whose options are the distinct providers present in `paymentMethodSummary` across loaded ads; selecting one re-lists with the `provider` param.
   - Render payment chips on each `AdCard` from `paymentMethodSummary`.
   - Replace all `NGN`/`₦`/"for NGN" literals with the selected currency's `code`/`symbol` from `CurrencyContext`.
   - Empty state copy: "No {token} sellers for {currency.code} yet" + existing Post-an-ad CTA.
3. **`components/p2p/PaymentMethodFilter.tsx`** (new) — small dropdown/sheet listing "All Payment Methods" + the available providers; controlled by parent.
4. **`app/p2p/create-ad.tsx`** — add a currency selector (default = trader's country currency); filter the trader's saved payment methods to the chosen currency; disable the float rate option + Bybit suggestion when the chosen currency has `floatAnchor: false`. Remove the NGN default.
5. **`app/p2p/payment-methods/new.tsx`** — add a provider picker (from `GET /p2p/payment-providers`); the account-field label follows the provider's `IdentifierLabel`; NG keeps the Nomba name-enquiry path, other currencies use manual entry (already supported via the country→currency mapping).
6. **`services/p2p.ts`** —
   - `P2PPaymentMethod`: add `provider: string`; widen `type` to `'bank' | 'mobile_money' | 'wallet'`.
   - `P2PAd`: add `paymentMethodSummary?: { provider: string; label: string }[]`.
   - `ListAdsFilter`: add `provider?: string`.
   - `paymentMethodsApi`: add `providers()` → `GET /p2p/payment-providers`; `create()` accepts `provider`.
   - Rates type: add `floatAnchor?: boolean` per currency.

---

## Error handling & edge cases

- **Unknown/empty currency rates:** fixed-rate ads display fine for any OER-covered currency. If a currency lacks an OER rate entirely, fixed ads still list (rate is the ad's own stored value); only float ads need an anchor.
- **Float ads in a non-anchored currency:** create-ad disables float for those currencies; any pre-existing float ad in such a currency lists with the existing `rateStale` badge and blocks trade-open (current behavior, unchanged).
- **Provider not in catalog (legacy/forward-compat):** frontend renders the raw slug title-cased as a fallback label; backend rejects create with an unknown provider (422).
- **Empty currency market:** "Post an ad" empty state; no payment-method filter options shown.
- **Privacy:** list never returns account numbers; only `{provider, label}`. Verified by the summary-only decoration path.

---

## Testing

**Backend (Go):**
- Provider catalog lookups (label/type/unknown).
- Create payment-method: valid provider persists + derives `type`; unknown provider → 422.
- Backfill: bank→`bank_transfer`; mobile_money name-match; idempotent re-run.
- Ad-list: `provider` filter returns only matching ads; `paymentMethodSummary` present, deduped, and contains **no** account fields.
- Rates response includes `floatAnchor` flags.

**Frontend (Jest):**
- `useP2pCurrency`: defaults from CurrencyContext on first run; persists + rehydrates selection.
- Ad-list maps `paymentMethodSummary` → chips; payment filter derives distinct providers and re-queries with `provider`.
- Create-ad: float disabled for non-anchored currency; currency switch re-filters payment methods.

**Manual (Expo):** switch currency NGN→CAD on the P2P screen; confirm ads/empty-state/chips/filter behave; create an ad in a non-NGN currency; add a `paypal` payment method and see it as a filterable chip.

---

## Build sequence

1. Backend: provider field + catalog + backfill + create-handler validation.
2. Backend: ad-list `provider` filter + `paymentMethodSummary` decoration; `floatAnchor` in rates.
3. Backend: `GET /p2p/payment-providers`.
4. Frontend: `services/p2p.ts` type/endpoint updates.
5. Frontend: `useP2pCurrency` + currency pill + de-NGN cleanup on browse.
6. Frontend: payment filter + ad-card chips.
7. Frontend: create-ad currency + float gating; add-method provider picker.
8. Tests + manual pass.
