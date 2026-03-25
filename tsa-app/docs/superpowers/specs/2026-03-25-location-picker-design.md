# LocationPicker Component Design

## Problem

Location input across the app is inconsistent — product listing uses free text, profile edit uses a country-only dropdown, delivery uses 3 separate dropdown components with inline API calls, and checkout is read-only. This breaks shipping zone detection (which compares buyer vs seller city/state/country) because free-text inputs produce mismatched strings like "Lagos" vs "Lagos State".

## Solution

A single reusable `LocationPicker` component that renders 3 cascading searchable dropdowns (Country → State → City), manages the CountriesNow API calls internally, and replaces all existing location inputs.

## Decisions

- **Seller location:** Defaults to profile-based; can be overridden per-product during listing
- **Buyer location at checkout:** Editable, pre-filled from profile, with confirmation before payment
- **Data source:** CountriesNow API (already used in delivery form) for states and cities; static country list from `constants/api/statesConstants.js`
- **No map integration needed** — cascading dropdowns solve the consistency problem

## Component API

```tsx
interface LocationPickerProps {
  value: { country: string; state: string; city: string };
  onChange: (location: { country: string; state: string; city: string }) => void;
  required?: ('country' | 'state' | 'city')[];
  labels?: { country?: string; state?: string; city?: string };
  showLabels?: boolean;
  disabled?: boolean;
  errors?: { country?: string; state?: string; city?: string };
}
```

### Props

| Prop | Type | Default | Description |
|------|------|---------|-------------|
| `value` | `{ country, state, city }` | required | Current selected location |
| `onChange` | `(location) => void` | required | Called when any field changes |
| `required` | `string[]` | `['country', 'state', 'city']` | Which fields are required |
| `labels` | `{ country?, state?, city? }` | `"Country" / "State" / "City"` | Custom label text |
| `showLabels` | `boolean` | `true` | Whether to show labels above dropdowns |
| `disabled` | `boolean` | `false` | Disables all dropdowns (for read-only contexts) |
| `errors` | `{ country?, state?, city? }` | `undefined` | External validation error messages |

## Internal Architecture

### SearchableDropdown (sub-component)

One internal component rendered 3 times with different data. Not exported.

**Behavior:**
- Tap to open modal with search input + scrollable list
- Type to filter (case-insensitive)
- Select item → closes modal, calls callback
- Loading spinner when data is being fetched
- "No results" when list is empty and not loading
- Disabled state when parent field not yet selected (e.g., "Select country first")

**Styling:** Uses `COLORS`, `SIZES` from `constants/theme.js`. Adopts the bottom-sheet modal pattern from `dropdowns.tsx` (the more polished variant) — with header, close button, search icon, checkmark on selected item, and empty state.

**Accessibility:** Auto-focuses search input on modal open. Dropdown trigger gets an `accessibilityLabel` (e.g., "Select Country"). Keyboard dismisses on selection.

### API Layer (`locationApi.ts`)

Separate utility file for fetch logic + in-memory cache.

**API endpoints (CountriesNow):**
- `POST /countries/states` → states for a country
- `POST /countries/state/cities` → cities for a country+state

**Country list:** Static from `constants/api/statesConstants.js`. No API call.

**Cache:** In-memory `Map` keyed by `"states:{country}"` and `"cities:{country}:{state}"`. Session-scoped, no persistence, no TTL. This is acceptable because geographic data rarely changes and the cache resets on app restart.

**Error handling:**
- Network failure → inline error with "Tap to retry" on the affected dropdown
- Empty response → "No results found"
- **API unavailability fallback:** If CountriesNow is unreachable after retry, the dropdown falls back to a free-text input so the user is not blocked. This is important because CountriesNow is a free community API with no SLA.

### Cascade Logic

1. On mount: if `value.country` set, fetch states (state/city dropdowns show "Loading..." during fetch)
2. Country changes → fetch states, clear state + city
3. State changes → fetch cities, clear city
4. **`onChange` fires once per user action** with the full location object. When country changes, a single `onChange({ country: "Nigeria", state: "", city: "" })` is emitted — not three separate calls. This matters for consumers that trigger side effects like shipping cost recalculation.

## New Files

| File | Purpose |
|------|---------|
| `components/common/LocationPicker.tsx` | The reusable component |
| `components/common/locationApi.ts` | CountriesNow API calls + cache |

## Files to Update

| File | Change |
|------|--------|
| `components/profile/EditProfile.tsx` | Replace native `Picker` country-only dropdown → full `LocationPicker` |
| `components/onboarding/Signup.tsx` | Replace `CustomPickerWithSearch` import → `LocationPicker` (country only, `required={['country']}`) |
| `components/orders/delivery.tsx` | Replace 3 dropdowns + inline API calls → `LocationPicker` |
| `app/merchants/inventory/add/index.tsx` | Replace free-text "Location" → `LocationPicker` (country/state/city). **Interim:** concatenate to single `location` string for API until backend adds structured fields |
| `app/merchants/inventory/edit/[productId].tsx` | Same as add |
| `app/(dashboard)/(tabs)/(home)/checkout/index.tsx` | Make shipping address editable with `LocationPicker`, pre-filled from profile, confirmation before payment |

## Files to Delete

| File | Reason |
|------|--------|
| `components/country/dropdown.tsx` | Replaced by `LocationPicker` |
| `components/country/dropdowns.tsx` | Replaced by `LocationPicker` (polished variant — its styling pattern is adopted into the new component) |
| `components/country/statedropdown.tsx` | Replaced by `LocationPicker` |
| `components/country/citydropdown.tsx` | Replaced by `LocationPicker` |

## Files Unchanged

| File | Reason |
|------|--------|
| `components/country/phoneNumber.tsx` | Unrelated, stays |
| `constants/api/statesConstants.js` | Country list still used; remove `nigeriaStatesAndLGAs` export (dead code cleanup) |

## Product Model Consideration

Product listing forms will send `country`, `state`, `city` instead of a single `location` string. The Go backend product model currently has no structured location fields for the product's origin — only the 4 shipping cost fields. A backend update to add `country`/`state`/`city` to the product model is a prerequisite for full integration but is a separate task.

**Interim behavior:** Until the backend is updated, the product listing forms will concatenate the selected values into a single `location` string (e.g., `"Lagos, Lagos, Nigeria"`) for the existing API field. This keeps the frontend and backend deployable independently.

## Checkout Flow

Currently read-only from profile. Will become:
1. Pre-fill `LocationPicker` with `currentUser.country/state/city`
2. User can edit any field
3. Address confirmation card shown before payment step
4. Shipping cost updates dynamically when location changes (using existing shipping estimate endpoint)
