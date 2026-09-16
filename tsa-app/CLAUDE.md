# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

**TSA Connect** — A React Native + Expo marketplace and wallet app with three user roles: Admin, Merchant, and User. Each role has its own dashboard and navigation.

## Commands

```bash
# Development
npx expo start              # Start dev server
npx expo start --ios        # iOS simulator
npx expo start --android    # Android emulator
npx expo start --web        # Web browser

# Testing & Linting
npm test                    # Jest (watch mode)
npx jest path/to/test.ts    # Run single test
npm run lint                # Expo lint (ESLint)
```

No separate build step — uses Expo's managed workflow. EAS Build profiles are in `eas.json`.

## Architecture

### Routing (Expo Router v6 — file-based)

```
app/
├── index.tsx                    # Entry: auth check → role-based redirect
├── login.tsx / signup.tsx       # Auth screens
├── (dashboard)/(tabs)/          # User tabs: Home, Marketplace, Services, Easyswap, Wallet
├── (detail)/                    # Screens owned by NO tab — see "Tab ownership" below
├── merchants/                   # Merchant panel
│   └── dashboard, inventory, profile, buy-usdt, sell-usdt, digital
├── profile/                     # User profile management
├── category/                    # Category browsing
└── adverts/                     # Product listings
```

Admin operations live in the standalone web dashboard (`tsa-admin`), not in the app — there is no in-app admin panel.

Role-based routing: `index.tsx` checks stored role → redirects to `/merchants/dashboard` or `/(dashboard)/(tabs)/(home)`.

#### Navigation Gotchas

- **Route groups `()` are transparent in URLs** — Always navigate with simplified paths: `` router.push(`/product/${id}`) `` NOT `router.push('/(dashboard)/(tabs)/(home)/product/[productId]')`. Use string template URLs, not `{ pathname, params }` objects.
- **Typed routes generated at** `.expo/types/router.d.ts` — regenerate with `npx expo start` if routes seem missing.

#### Back navigation — three rules

These were all violated at once before `feat/nav-back-flow`; `__tests__/route-back-affordance.test.ts`
now fails the build if the first two regress.

**1. Tab ownership decides where back goes.** expo-router resolves a push by navigating to the
navigator that OWNS the route. A screen inside `(tabs)/(home)` pushed from the Marketplace tab
switches to Home first, so back returns to Home — not to the marketplace the user came from.
Anything reachable from more than one tab therefore lives in **`app/(detail)/`**, a root-level
group: `product/[productId]`, `seller/[sellerId]`, `subcategory/[subcategoryId]`, `checkout`,
`fund`, `send`, `swap`, `multiswap`, `transfer`, `trade`, `rewards`, `cashback`, `service`,
`serviceaction`. A tab group should contain its root screen and little else. Group parentheses
keep URLs identical, so moving a screen in or out of `(detail)` needs no call-site changes —
verify with `.expo/types/router.d.ts` before and after.

**2. A nested stack entered from outside has no native back button.** `getStateFromPath` builds a
single-route state for the target group (expo-router `fork/getStateFromPath.js` → `createStateObject`),
so native-stack draws nothing at index 0 even though `router.back()` unwinds correctly. Every Stack
layout therefore takes its `screenOptions` from **`useDefaultScreenOptions()`**
(`constants/navigation.ts`), which supplies a shared `headerLeft`. Do not hand-roll `screenOptions`
in a layout, and do not re-declare `headerTintColor` / `headerTitleStyle` anywhere.

**3. Back is `goBack(fallback)`, never a fixed route.** `utils/back.ts` pops when there is history
and only falls back on a cold start (deep link, notification tap, OAuth callback). The fallback
names the SECTION (`WALLET_TAB`, `SERVICES_TAB`, `MARKETPLACE_TAB` from `constants/routes.ts`) —
defaulting it to `/home` is what produced the "back takes me to main" reports. Screens that hide
their header must render **`<ScreenHeader>`**; never add a tenth hand-rolled chevron row.

Drawer screens (`app/(dashboard)/*`) get a hamburger, never a back chevron, so a push-only
destination does not belong there — `orderdetail` and `add-category` are root Stack routes for
exactly this reason.

### State Management

- **`AuthContext/AuthContext.tsx`** — Primary context: auth state, user data, token management via AsyncStorage
- **`AuthContext/AppContext.tsx`** — Secondary context: cart management (`addItem`/`removeItem`/`getItems`), category selection, app service state

No Redux/Zustand — pure Context API + AsyncStorage.

### API Layer

- **`constants/api/apiClient.js`** — Axios instance, base URL: AWS API Gateway
- **`components/services/api.ts`** — Main API service (~1500 lines, 40+ endpoints): auth, users, categories, adverts, cart, orders, payments, file uploads, merchant/admin operations
- Auth: JWT Bearer tokens stored in AsyncStorage, set on `axios.defaults.headers.common['Authorization']`
- **API base URL**: `https://tsa.mcgpchain.com/api` (both dev and prod in `constants/api/config.ts`)

#### API Gotchas

- **Backend uses PostgreSQL with UUIDs** — Go backend (tsa-api-go) returns `id` (not `_id`) for all entities. Frontend interfaces historically used `_id` but `id` is the correct field from the API. Always prefer `id` over `_id` when accessing entity identifiers.
- **Product data shape differs from type** — `category` is a UUID string (not an object), `images` may lack `url` field (only have `id`), `rating` can be null, `attributes` is optional. Always null-check these fields.
- **Currency**: App uses `$` (USD), not `₦` (Naira)

### Key Conventions

- **Path alias**: `@/*` maps to project root (configured in `tsconfig.json`)
- **Design tokens**: `constants/theme.js` exports `COLORS`, `SIZES`, `FONTS`, `SHADOWS`
- **Types**: `types/marketplace.ts` for Seller, Product, Subcategory interfaces
- **Custom hooks**: `hooks/` directory — `useSellerData`, `useSubcategoryProducts`, `useSubcategorySellers`
- **Components**: `components/` organized by feature (marketplace, products, orders, services, signup, etc.)

### Tech Stack

- React 19 + React Native 0.81 + Expo 54
- expo-router v6 (typed routes enabled)
- react-native-reanimated + worklets
- axios for HTTP
- AsyncStorage for persistence
- @react-navigation (drawer, material-top-tabs)
- expo-image-picker, expo-camera for media
- moment for dates