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
├── admin/                       # Admin panel (drawer navigation)
│   └── dashboard, category, advert-request, deposit-requests, profile
├── merchants/                   # Merchant panel
│   └── dashboard, inventory, profile, buy-usdt, sell-usdt, digital
├── profile/                     # User profile management
├── category/                    # Category browsing
└── adverts/                     # Product listings
```

Role-based routing: `index.tsx` checks stored role → redirects to `/admin/dashboard`, `/merchants/dashboard`, or `/(dashboard)/(tabs)/(home)`.

#### Navigation Gotchas

- **Route groups `()` are transparent in URLs** — Always navigate with simplified paths: `` router.push(`/product/${id}`) `` NOT `router.push('/(dashboard)/(tabs)/(home)/product/[productId]')`. Use string template URLs, not `{ pathname, params }` objects.
- **Dynamic routes in `(home)`** — `product/[productId]`, `seller/[sellerId]`, `subcategory/[subcategoryId]` are registered in `app/(dashboard)/(tabs)/(home)/_layout.tsx`
- **Typed routes generated at** `.expo/types/router.d.ts` — regenerate with `npx expo start` if routes seem missing

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

- **Backend uses UUIDs, not MongoDB ObjectIds** — Product `id` field is a UUID. The `/products/:id` GET endpoint rejects UUIDs ("Invalid product ID"). Workaround: pass product data via navigation params instead of re-fetching.
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