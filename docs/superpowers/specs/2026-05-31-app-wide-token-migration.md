# App-wide migration to TSA Signature tokens

**Status:** planned — awaiting de-bold sign-off (PR #25) before execution.
**Goal (user, #34):** migrate the *entire* app off legacy `constants/theme.js`
(`COLORS/SIZES/FONTS/SHADOWS`) + ad-hoc `StyleSheet`/hex onto `@/theme` tokens +
`@/components/ui` primitives — yielding one consistent look and **dark-mode coverage**.

## Scope reality

- **~117 `.tsx` screens.** Only 24 import `constants/theme`; the rest hardcode
  styles inline. So this is a large, phased program (≈10× the P2P redesign).
- Legacy `constants/theme.js` is cruft: duplicate keys (`background` defined as
  `#253334` then `#f9fafb`; `white`, `gray`, `font` doubled) — JS last-wins, so
  the *effective* palette is light (`background #f9fafb`, `white #fff`,
  `gray #6b7280`, `primary #9D6B38`, semantic success/warning/danger).

## Strategy: shim first, then per-screen

**Phase 0 — Foundation (low risk, high leverage):**
1. **Color bridge.** Rewrite `constants/theme.js` so `COLORS/SIZES` *derive their
   values from the light token palette* (`@/theme` `lightColors` + `spacing`/
   `radius`). Net effect: every legacy screen instantly adopts the new light
   palette with zero per-screen edits and ~zero risk. (Static values → light
   only; dark mode still requires per-screen migration in later phases.)
2. **Shared components.** Migrate the widely-used shared components to `useTheme()`
   + primitives so downstream screens inherit the look (and dark mode) for free:
   `components/buttons/*`, `components/onboarding/*` (8), `components/signup/*`,
   `components/common/*`, `components/category/*`, common headers.

**Phases 1–4 — Per-screen migration (adds dark mode + primitives):**
each screen: replace `StyleSheet`/`COLORS` with `useTheme()` tokens + primitives,
**logic preserved verbatim**, then adversarial logic-preservation review (the
exact workflow that delivered the P2P screens). Order (user choice = main tabs first):

- **Phase 1 — `(dashboard)` tabs:** Home, Marketplace, Services, Easyswap, **Wallet** (~38 screens incl. nested).
- **Phase 2 — `merchants/`** (13).
- **Phase 3 — auth + onboarding + signup.**
- **Phase 4 — profile, category, adverts, `(productgroup)`, orders, admin.**

Each phase ships as its own reviewed PR(s) into `feat/p2p-trading`.

## Effective legacy → token mapping (for the Phase-0 bridge)

| legacy `COLORS.*` | → `@/theme` light token |
|---|---|
| `primary` `#9D6B38` | `colors.accent` / brand (gold `#C8961E` accent; brown `#9D6B38` stays for rails) |
| `background` `#f9fafb` | `colors.background` |
| `white` `#fff` | `colors.surface` |
| `dark` `#1f2937` | `colors.textPrimary` |
| `gray` `#6b7280` | `colors.textSecondary` |
| `lightGray` `#e5e7eb` | `colors.border` / `colors.surfaceAlt` |
| `success/warning/danger` | `colors.success/warning/danger` |
| `SIZES.base/padding*` | `spacing.*` | `SIZES.radius` | `radius.*` |

(Exact token names verified against `theme/colors.ts` at execution time.)

## Risks / notes

- **Dark mode is staged:** the Phase-0 shim is light-only (StyleSheet is static);
  a legacy screen won't go dark until its phase migrates it to `useTheme()`. The
  toggle already works app-wide for migrated screens.
- **Complex screens** (wallet send/receive, easyswap, cart/checkout) carry real
  logic — these get the same logic-preservation review as the P2P cockpit.
- **Do not** start screen phases until the de-bold direction is locked, to avoid
  re-touching 100 screens.

## Verification per phase
`tsc --noEmit` (no new errors) · `eslint` (clean for touched files) · `jest` ·
adversarial logic-preservation review per screen.
