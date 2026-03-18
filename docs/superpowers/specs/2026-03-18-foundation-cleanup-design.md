# Sub-project 1: Foundation Cleanup — TypeScript Strictness & Dead Code

## Overview

Remove all 133 `@ts-ignore` directives from the mobile app (`tsa-app`) and clean up the AuthContext dead code. This eliminates suppressed type errors that hide real bugs, improves IDE support, and prevents future regressions.

## Decisions

- **No behavior changes**: Every fix is a type-level correction. Runtime behavior stays identical except for 2 actual bugs found.
- **FormData augmentation**: Use a global `.d.ts` file to extend `FormData.append()` for React Native's non-standard blob objects. Uses `value: any` intentionally — React Native's multipart handling accepts plain objects `{ uri, type, name }` which don't fit a narrower union without importing RN internals. This is a pragmatic tradeoff.
- **Router typed routes**: Use `as Href` cast for dynamic paths rather than rewriting all navigation to use parameterized forms. The codebase has many dynamically constructed routes that can't be statically verified.
- **Prop interfaces**: Add inline prop types where components are small. Don't create separate type files for one-off prop shapes.
- **`string | undefined` → default values**: Initialize state with `""` instead of leaving as `undefined` where the consuming component requires `string`.

## Scope

### 1. AuthContext Cleanup (`AuthContext/AuthContext.tsx`)

- Delete commented-out old `signup` function (lines 172-200)
- Delete the local `register` function (lines 390-396) that shadows the imported `register` from `AuthenticationService`
- Remove the "Also update your register API function" comment (line 388)
- Remove the `@ts-ignore` on `router.push("/login")` — use `as Href`
- Keep the `expo-file-system` import — it is used by the `appendFile` helper inside the live `signup` function

### 2. FormData Augmentation (39 ignores)

Create `tsa-app/types/react-native-formdata.d.ts`:
```typescript
declare global {
  interface FormData {
    append(name: string, value: any, fileName?: string): void;
  }
}
export {};
```

Then for each FormData call site:
- Remove the `@ts-ignore`
- Convert numbers to strings: `formData.append("price", String(price))`
- Convert booleans to strings: `formData.append("isFeatured", String(isFeatured))`
- Image blob objects will work via the augmentation without any code change

**Files:** `components/products/AddProduct.tsx`, `components/services/AddService.tsx`, `components/category/AddCategory.tsx`, `components/category/EditCategory.tsx`, `components/profile/EditProfile.tsx`, `components/orders/paymentproof.tsx`, `screens/sellP2P.tsx`, `app/profile/edit-advert.tsx`, `app/merchants/inventory/add/index.tsx`, `components/services/api.ts`

### 3. Router Typed Routes (23 ignores)

For all `router.push()` / `router.replace()` / `href=` calls:
- Import `Href` from `expo-router`
- Replace `@ts-ignore` + string literal with cast: `router.push("/home" as Href)`
- For object-form: `router.push({ pathname: "/path", params } as Href)`

**Files:** `app/order.tsx`, `app/recovery.tsx`, `app/payments.tsx`, `app/signup.tsx`, `app/verify.tsx`, `app/passwordOTP.tsx`, `components/appservices/AppServices.tsx`, `components/marketplace/tab.tsx`, `components/features/Card.tsx`, `components/category/AddCategory.tsx`, `components/category/EditCategory.tsx`, `AuthContext/AuthContext.tsx`, `screens/sellP2P.tsx`, `app/adverts/products/[productCategory]/index.tsx`, `components/accessories/ListCard.tsx`, `components/accessories/ProductListing.tsx`, `components/accessories/ProductListCard.tsx`, `components/services/servicelist.tsx`

### 4. Untyped Component Props (20 ignores)

Add prop interfaces to components that destructure props without types:
- `components/products/productdetail.tsx` — `{ item: Product }`
- `components/profile/EditProfile.tsx` — `{ user: User }`
- `components/orders/paymentproof.tsx` — `{ id: string }`
- `components/orders/bankdetails.tsx` — `{ accountNumber: string, accountName: string, bankName: string, id: string }`
- `components/services/ServiceCard.tsx` — `{ id: string, title: string, image: string, description: string }`
- `components/marketplace/tab.tsx` — `{ link: string, buttonTitle: string, active: boolean }`
- `components/services/servicelist.tsx` — type `item` properties in map callbacks
- `app/(servicegroup)/categoryservice.tsx` — type `item` properties passed to ServiceCard (5 ignores on lines 33, 43, 57, 59, 61, 63)

Also type function parameters like `addItem(newItem: any)`, `checkItemExist(name: string)`.

### 5. Third-Party Prop Mismatches (18 ignores)

- **Phone number component**: Import `Country` type from `react-native-international-phone-number` instead of redeclaring locally
- **Picker.Item**: Type the data arrays so `item.title` and `item.id` are known strings
- **TextInput value**: Add `?? ""` fallback: `value={productName ?? ""}`
- **Custom dropdowns**: Fix state initialization from `undefined` to `""`

**Files:** `components/country/phoneNumber.tsx`, `components/orders/delivery.tsx`, `components/profile/EditProfile.tsx`, `components/services/AddService.tsx`, `components/category/AddCategory.tsx`, `components/category/EditCategory.tsx`, `app/profile/edit-advert.tsx`

### 6. API Response Types (15 ignores)

For each file, the specific fixes:
- `components/signup/signupflow.tsx` — type `result` from `signup()` as `{ success: boolean; error: boolean; message: string; data?: any }`; type `result.data.token` and `result.data.userId` access
- `components/orders/PaymentType.tsx` — type the error catch block with optional chaining
- `app/adverts/products/[productCategory]/index.tsx` — type `response.data.products` access
- `app/merchants/inventory/index.tsx` — type `response.data?.products` access
- `app/profile/verify-email.tsx` — type error catch with `error?.response?.data?.message`
- `app/(servicegroup)/categoryservice.tsx` — type `services` state array with interface including `category` field

### 7. Navigation Callbacks (4 ignores)

In `app/(dashboard)/_layout.tsx`, all 4 ignores:
- **Line 103**: `screenOptions` callback — type the parameter: `({ route }: { route: { name: string } })`
- **Line 105**: `drawerIcon` callback — type: `({ color, size }: { color: string; size: number })`
- **Line 144**: `name={iconName}` on `Icon` component — initialize `let iconName: string = "home"` with a default value, then cast at usage: `name={iconName as any}` (the icon name union from `@expo/vector-icons` is too large to type manually and changes per icon set)
- **Line 174**: second icon callback — same pattern as line 105/144

### 8. Bug Fixes (2 real bugs)

**Bug 1 — `components/category/EditCategory.tsx`**: `Alert.alert({type: "error", text1: "..."})` uses object syntax (react-native-toast-message API) instead of `Alert.alert("Error", "Title and type are required")`. Fix to correct `Alert.alert()` signature.

**Bug 2 — `components/designs/productcard.tsx`**: `assets.location` references a missing icon in the `constants/assets.js` export. The `@ts-ignore` is also misspelled as `@ts-ignores` (not a valid directive, so TS is likely already erroring). Fix: check if a location icon exists in `constants/assets.js` — if not, either add one or remove the `<Image>` usage.

### 9. Miscellaneous (8 ignores)

- `app/(servicegroup)/categoryservice.tsx:44` — type `services` state array (covered in Section 6)
- `app/(productgroup)/cart/index.tsx:340` — wrap in `!!()` to force boolean for `disabled` prop
- `app/admin/advert-request/index.tsx:309,316` — add `merchant?: { name: string }` to item type
- `app/admin/category/add/index.tsx:192` — add `isActive: boolean` to form state type
- `app/admin/dashboard/index.tsx:122` — type `stat.gradient` as `[string, string]`
- `app/profile/edit-advert.tsx:30` — narrow `string | string[]` from `useLocalSearchParams()`: `const images = typeof item.images === 'string' ? item.images : item.images?.join(',') ?? ''`
- `components/designs/DetailsDesc.tsx:43` — add `subTitle` to component prop interface
- `components/orders/delivery.tsx:39` — use correct `Country` type import from the library

## Testing

- **Baseline**: Before starting work, run `npx tsc --noEmit 2>&1 | wc -l` and record the baseline error count. Any increase is a regression.
- **TypeScript check**: `npx tsc --noEmit` in `tsa-app` — error count must not increase from baseline
- **Build check**: `npx expo export --platform web` to verify the app bundles
- **No runtime changes** except for the 2 bug fixes
