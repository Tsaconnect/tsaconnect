# EditProfile Screen Redesign

## Problem

The EditProfile screen has UX/UI issues:
1. **Wrong fields** — shows LocationPicker (country/state/city) which is unnecessary since shipping address is set at checkout. Missing username and address fields.
2. **Redundant country code selector** — the PhoneNumber component has its own country picker that duplicates location selection. The country code should auto-derive from the user's country.
3. **Broken layout** — uses absolute positioning (`position: 'absolute', top: SIZES.height * 0.1212`) which is fragile across screen sizes. No ScrollView, so keyboard hides fields.
4. **No visual hierarchy** — flat list of inputs with no spacing rhythm or section structure.

## Solution

Redesign EditProfile as a clean, minimal form with the right fields, proper scrolling, and polished styling.

## Fields

| Field | Type | Source | Required |
|-------|------|--------|----------|
| Profile picture | Image picker | `user.profilePicture` | No |
| Full Name | TextInput | `user.name` | Yes |
| Username | TextInput | `user.username` | No |
| Phone Number | PhoneInput (existing lib) | `user.phoneNumber` | No |
| Address | TextInput | `user.address` | No |

## Layout

**Style: Clean Minimal** — simple stacked inputs with labels above, no section grouping.

1. **Avatar** — centered at top, circular, with camera icon overlay (bottom-right). Tap anywhere on avatar to open image picker. Shows user initial as fallback.
2. **Inputs** — each has a label above and a rounded bordered input below. Consistent spacing (16px gap between fields).
3. **Phone Number** — uses the existing `react-native-international-phone-number` PhoneInput component (already in the app via `PhoneNumber` component). It has a built-in country code picker. We'll set its default country from `user.country` using the `defaultCountry` prop.
4. **Save Changes button** — full-width at bottom, primary color, with loading spinner during API call.
5. **Wrapped in** `KeyboardAvoidingView` + `ScrollView` for proper keyboard handling.

## Phone Number Behavior

The existing `react-native-international-phone-number` library's `PhoneInput` component already handles:
- Country flag + calling code display
- Built-in country picker modal on tap
- Phone number formatting

**Change from current:** Instead of managing `selectedCountry` as separate state with no default, we'll pass `defaultCountry` prop (ISO country code, e.g., `"NG"` for Nigeria) derived from `user.country`. This auto-sets the calling code on mount. User can still tap to override.

**Country name → ISO code mapping:** Add a small utility mapping (e.g., `"Nigeria"` → `"NG"`) using the existing `countries` list from `statesConstants.js`. The `react-native-international-phone-number` library uses ISO 3166-1 alpha-2 codes.

## What Gets Removed

- `LocationPicker` import and usage (country/state/city dropdowns)
- `location` state object
- `react-native-elements` `Avatar` import — replace with plain `View`/`Image`
- Absolute positioning layout (`position: 'absolute', top: SIZES.height * 0.1212`)
- `cover` style and rigid positioning

## What Gets Added

- `username` state, pre-filled from `user.username`
- `address` state, pre-filled from `user.address`
- `KeyboardAvoidingView` + `ScrollView` wrapper
- `defaultCountry` derivation for PhoneInput
- Loading state on Save button
- Inline validation (name required)

## API Changes

`handleUpdate` sends to `PATCH /users/:id`:
- `name` — from name input
- `username` — from username input
- `phoneNumber` — combined calling code + number (existing behavior)
- `address` — from address input
- `profilePicture` — as FormData file if changed

**Removed from PATCH:** `country`, `state`, `city` — no longer edited here.

## Files to Modify

| File | Change |
|------|--------|
| `components/profile/EditProfile.tsx` | Full rewrite — new layout, new fields, remove LocationPicker |

## Files Unchanged

| File | Reason |
|------|--------|
| `components/country/phoneNumber.tsx` | Still used, no changes needed — EditProfile will use `PhoneInput` directly for more control over `defaultCountry` prop |

## Styling

Uses `COLORS`, `SIZES` from `constants/theme.js`:
- Background: `#fff`
- Input borders: `#e0e0e0`, border-radius 10
- Labels: `#666`, 13px, font-weight 500
- Button: `COLORS.primary`, border-radius 12, 15px font
- Avatar: 90px, `COLORS.primary` background for initial fallback, white text
- Camera icon overlay: 28px circle, white background, bottom-right of avatar
- Field spacing: 16px between each field group
