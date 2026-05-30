# TSA Signature — P2P Design System

**Date:** 2026-05-30 · **Branch:** `feat/p2p-trading` · **Task:** #20 (foundation for #21 redesign + all P2P feature UIs)
**Status:** reviewed by 2 independent agents (RN/Expo feasibility + completeness); their fixes are incorporated below.

## Goal

A single, adaptive (light + dark) design system — "TSA Signature" — that makes the P2P
suite feel intentional and premium (Bybit/Telegram-P2P quality) while staying uniquely
TSA. It is the foundation every P2P screen and new feature UI builds on.

**Design language (locked with the user):** accent-rail cards, adaptive light+dark, bold
confident numerals, 16px rounded cards, spring-press micro-interactions. Brand accent is
**gold** (`#D4AF37`) over warm-brown (`#9D6B38`) heritage; on deep-slate dark the gold pops.

## Hard constraints

1. **Rebuild-free.** Use only native deps already in the binary — reanimated **v4** (proven
   live in `screens/all-swap.tsx`), gesture-handler, react-native-svg, expo-font,
   safe-area-context — plus **JS-only** adds: `@expo-google-fonts/inter` (runtime),
   `@testing-library/react-native` (dev). Add both with `npx expo install …`. **No** new
   native modules (expo-notifications/haptics/blur/@gorhom/bottom-sheet) in this task.
   - reanimated v4 requires the **new architecture**. `app.json` has `newArchEnabled`
     mis-nested under `expo.splash`; move it to top-level `expo.newArchEnabled: true`
     (a config edit, applied on next OTA/build — *not* a native rebuild; new arch is already
     effectively on since `all-swap.tsx` animates in the shipped app).
   - reanimated v4's babel plugin is `react-native-worklets/plugin` and is **already** the
     last plugin in `babel.config.cjs`. Do NOT add `react-native-reanimated/plugin`.
   - Skeleton/loops use `withRepeat` and must honor `useReducedMotion()`.
2. **Additive, non-breaking.** New system lives under `tsa-app/theme/` + `tsa-app/components/ui/`.
   Do **not** restyle non-P2P screens. Legacy `constants/theme.js`, `constants/Colors.js`,
   `constants/p2p.ts` stay; the new system supersedes them for P2P (screens migrate in #21).
3. **Imports.** Use the existing `@/` path alias (tsconfig maps `@/*` → root). Primitives import
   the foundation via `@/theme`, never deep relative paths.
4. **Typed + accessible + global-ready.** Full TS; ≥44px touch targets; `accessibilityRole`/
   `accessibilityLabel` on interactive primitives; AA contrast for text on accent and for
   disabled states; cap `allowFontScaling` at 1.3×; honor reduced-motion. **Logical edges only**
   (`start`/`end`, never `left`/`right`) so the accent rail and chat bubbles flip under RTL
   (`I18nManager`). **Locale-aware numbers**: money/number formatting takes the active locale
   (no hardcoded `en-US`); `AmountText` standardizes symbol + grouped value + optional ISO code.
5. **Verifiable without a device.** `tsc` + lint + jest green; a UI gallery route for visual QA +
   screenshots; token light/dark parity + StatusPill coverage asserted in tests.

## Architecture

```
tsa-app/
  theme/
    tokens.ts          # raw scales: palette ramps, spacing, radius, typography, motion, z, gradients
    colors.ts          # semantic ColorTokens for light & dark (built from palette)
    ThemeProvider.tsx  # context; useColorScheme(); setScheme('light'|'dark'|'system'); default system
    useTheme.ts        # -> Theme {colors, spacing, radius, type, motion, gradients, scheme, isDark}
    fonts.ts           # Inter family map + useAppFonts() (@expo-google-fonts/inter via expo-font)
    format.ts          # locale-aware formatFiat/formatNumber/formatToken (active locale, not en-US)
    index.ts
  components/ui/
    Text.tsx           # variant typography; AmountText (tabular numerals + locale formatting)
    PressableScale.tsx # spring-press wrapper (reanimated v4); hapticStyle shim (no-op until expo-haptics)
    Button.tsx         # primary|secondary|ghost|danger|success * sm|md|lg; loading; left/right icon
    IconButton.tsx · Icon.tsx   # semantic-name -> @expo/vector-icons glyph (one registry; no svg needed)
    Surface.tsx · Card.tsx · AccentCard.tsx   # 3-tier; AccentCard = signature start-edge rail, 16px
    Input.tsx · AmountInput.tsx # Input: label/helper/error/counter/adornments. AmountInput: decimal-pad,
                                #   prefix/suffix symbol, "Max"/balance hint, parse/format wired
    Sheet.tsx          # bottom sheet on RN Modal + reanimated slide + scrim (tap/close to dismiss)
    SelectRow.tsx      # input-looking field (label+value+chevron) that opens a Sheet
    SearchableList.tsx # Sheet body: search input + (optional grouped/section) list + selected row + empty/loading
    RadioRow.tsx · RadioGroup.tsx   # single-select rows (PM picker, dispute reason, onboarding steps)
    SegmentedControl.tsx # Buy/Sell + generic; animated thumb
    Chip.tsx · Toggle.tsx        # filter/selectable chip; themed switch wrapper (auto-release ⚡, default PM)
    Badge.tsx          # base label primitive; TrustBadge (Verified/Trusted/New + ⚡) is a preset
    StatusPill.tsx     # NAMESPACE-AWARE: kind 'trade'|'ad'|'dispute' -> {label,color,bg}; thin preset over Badge
    Avatar.tsx         # initials/image + online dot
    Skeleton.tsx       # shimmer block (withRepeat, reduced-motion aware) + SkeletonGroup presets
    EmptyState.tsx · ErrorState.tsx   # consistent zero/error w/ retry
    Toast.tsx          # ToastProvider + useToast() (replaces Alert for success/info); top-anchored, queued
    Spinner.tsx        # ActivityIndicator + optional label ("Matching merchants…", "Releasing on chain…")
    CountdownBar.tsx   # payment-window bar; <5min urgent state
    SummaryRow.tsx     # label↔value/total rows (FeeBreakdown, order/onboarding summaries) + SummaryCard
    BottomActionBar.tsx# sticky CTA docked above safe area (forms, wizards, Express)
    Wizard.tsx         # multi-step shell: step header (N of M) + slide + BottomActionBar footer
    CopyableField.tsx  # tap-to-copy value + "Copied" toast (bank no., escrow address)
    PhotoPickerTile.tsx# add/preview/remove + upload progress (wraps existing expo-image-picker + Cloudinary)
    Divider.tsx
    index.ts
  app/p2p/_dev/ui-gallery.tsx   # kitchen-sink gallery (dev route) — all primitives × variants × both schemes
```

### Tokens (`theme/tokens.ts`)

- **palette** — brand gold ramp (gold50…gold900 around `#D4AF37`), brown ramp (`#9D6B38`),
  neutral/slate ramp (light greys → deep slate `#10181A`/`#16201F`), semantic success `#10b981`,
  warning `#f59e0b`, danger `#ef4444`, info `#3b82f6`, trade-side buy `#10b981` / sell `#f0584b`,
  and **escrow/locked** `#8b7cc8`→tuned neutral-violet (funds-frozen state).
- **spacing** `{xs:4, sm:8, md:12, lg:16, xl:20, '2xl':24, '3xl':32, '4xl':40}` · **radius**
  `{sm:8, md:12, lg:16, xl:20, pill:999}` (cards=lg).
- **type scale** — display 34/40·800, title1 28·700, title2 22·700, title3 18·600, body 16·400,
  bodyMed 16·600, bodySm 14·400, caption 12·500, overline 11·600. Numerals: Inter +
  `fontVariant:['tabular-nums']`.
- **motion** — `spring.press{damping:18,stiffness:320,mass:0.6}`, `spring.enter{damping:20,stiffness:200}`,
  `timing{fast:120,base:200,slow:320}`.
- **gradients** — `accentRail` (gold→brown) for the signature card rail/hero; flat fallback for AA.
- **z** — overlay/sheet/toast layering.

### Semantic colors (`theme/colors.ts`)

One `ColorTokens` interface, `light` + `dark` implementations. Keys: `background, backgroundAlt,
surface, surfaceAlt, surfaceElevated, surfaceDisabled, border, borderDisabled, hairline, overlay,
textPrimary, textSecondary, textMuted, textDisabled, textInverse, accent, accentHover, accentMuted,
accentDisabled, onAccent, accentRail, focusRing, success/successBg, warning/warningBg, danger/dangerBg,
info/infoBg, escrow/escrowBg, buy, sell, online`. A jest test asserts light & dark define the **same
key set**.
- **Light:** background `#FAFAF8`, surface `#FFFFFF`, accent `#C8961E` (AA on white for bold/large),
  text `#16201f`/`#5b6b6a`, focusRing accent@40%.
- **Dark:** background `#10181A`, surface `#16201F`, surfaceElevated `#1E2A29`, accent `#E5C04B`,
  text `#F2F4F3`/`#9FB0AE`.

### ThemeProvider / useTheme

Reads `useColorScheme()` (no existing scheme manager — ThemeProvider owns dark mode cleanly),
memoizes `Theme`, exposes `setScheme` for a future toggle (default `system`). Mounted in
`app/_layout.tsx` as the **outermost** wrapper; `ToastProvider` **innermost**, around `StatusBar`+`Stack`.
The hardcoded `StatusBar barStyle="dark-content" backgroundColor="#fff"` (breaks dark mode) becomes
theme-driven (or `expo-status-bar` `style="auto"`). Every primitive styles via `useTheme()` — no
hardcoded colors in `components/ui/*`.

### Fonts

`useAppFonts()` loads Inter 400/500/600/700/800; `app/_layout.tsx` holds the splash
(`SplashScreen.preventAutoHideAsync`) until ready (the existing layout loads no fonts, so this is
additive). `fonts.ts` maps weight→family (RN can't synthesize weights from one face).

### StatusPill — authoritative status sets (namespace-aware)

`StatusPill kind='trade'|'ad'|'dispute' status=…`. The mapping table (and its jest parity test) must
cover **exactly**:
- **trade (7):** `pending_payment, paid, releasing, released, cancelled, disputed, resolved`
  (`releasing` = off-chain intermediate, escrow hue; easy to miss).
- **ad (5):** `pending, active, paused, exhausted, cancelled`.
- **dispute (3):** `open, resolved, rejected`.
- **derived UI states** (not DB enums, but first-class on screens): trade `expired`
  (`pending_payment` past `expiresAt`) and ad `rateStale` (live rate unavailable).
`cancelled` and `resolved` mean different things across namespaces → the `kind` discriminator is
required so labels/colors don't collide.

## Testing & verification

- **jest**: token light/dark key-set parity; `useTheme` under mocked `useColorScheme`; StatusPill
  table covers all 15 statuses + 2 derived per namespace; `format.ts` locale cases; render smoke
  tests (`@testing-library/react-native`) for Button, AccentCard, StatusPill, Input, AmountInput,
  Sheet, Toast, Skeleton, Wizard.
- **tsc --noEmit** + **eslint** clean on touched files.
- **UI gallery** route renders every primitive × variants × both schemes; capture Expo-web
  screenshots for the PR (user's final visual sign-off).

## Scope notes

**Deferred (build when their feature lands, still under `components/ui`):** Timeline/ProgressTracker
(trade cockpit hand-rolled version works → #21), NotificationBanner (decide vs Toast in #27).
**Cut (YAGNI):** Slider, CountUp/animated number, chart/sparkline tokens.
**Out of scope:** non-P2P restyling; push/haptics/blur native deps; replacing react-native-elements
globally; full Storybook (gallery route stands in).

## Rollout

1. Foundation (sequential, me): `theme/*` + fonts + `app/_layout.tsx` wiring + `app.json` newArch +
   StatusBar fix + add the two JS deps.
2. Primitives (parallel subagents on the shared foundation): `components/ui/*` + their jest tests.
3. Gallery + full test/tsc/lint pass; screenshots.
4. PR → `feat/p2p-trading`; self-review; merge. Then #21 migrates screens.
