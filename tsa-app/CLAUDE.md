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

### State Management

- **`AuthContext/AuthContext.tsx`** — Primary context: auth state, user data, token management via AsyncStorage
- **`AuthContext/AppContext.tsx`** — Secondary context: cart management (`addItem`/`removeItem`/`getItems`), category selection, app service state

No Redux/Zustand — pure Context API + AsyncStorage.

### API Layer

- **`constants/api/apiClient.js`** — Axios instance, base URL: AWS API Gateway
- **`components/services/api.ts`** — Main API service (~1500 lines, 40+ endpoints): auth, users, categories, adverts, cart, orders, payments, file uploads, merchant/admin operations
- Auth: JWT Bearer tokens stored in AsyncStorage, set on `axios.defaults.headers.common['Authorization']`

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