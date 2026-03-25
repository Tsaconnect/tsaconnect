# LocationPicker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace all inconsistent location inputs with a single reusable `LocationPicker` component using cascading Country → State → City dropdowns powered by the CountriesNow API.

**Architecture:** One `LocationPicker` component containing an internal `SearchableDropdown` sub-component rendered 3 times. API logic lives in a separate `locationApi.ts` utility with in-memory caching. All existing location inputs across 6 screens are migrated to use this component, and the 3 old dropdown components are deleted.

**Tech Stack:** React Native, Expo, TypeScript, CountriesNow API (`countriesnow.space`), MaterialIcons

**Spec:** `docs/superpowers/specs/2026-03-25-location-picker-design.md`

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `components/common/locationApi.ts` | Create | CountriesNow API calls + in-memory cache |
| `components/common/LocationPicker.tsx` | Create | Reusable cascading Country → State → City picker |
| `components/onboarding/Signup.tsx` | Modify | Replace `CustomPickerWithSearch` → `LocationPicker` |
| `components/profile/EditProfile.tsx` | Modify | Replace native `Picker` → `LocationPicker` |
| `components/orders/delivery.tsx` | Modify | Replace 3 dropdowns + inline API → `LocationPicker` |
| `app/merchants/inventory/add/index.tsx` | Modify | Replace free-text Location → `LocationPicker` |
| `app/merchants/inventory/edit/[productId].tsx` | Modify | Replace free-text Location → `LocationPicker` |
| `app/(dashboard)/(tabs)/(home)/checkout/index.tsx` | Modify | Make shipping address editable with `LocationPicker` |
| `components/country/dropdown.tsx` | Delete | Replaced by `LocationPicker` |
| `components/country/dropdowns.tsx` | Delete | Replaced by `LocationPicker` |
| `components/country/statedropdown.tsx` | Delete | Replaced by `LocationPicker` |
| `components/country/citydropdown.tsx` | Delete | Replaced by `LocationPicker` |
| `constants/api/statesConstants.js` | Modify | Remove unused `nigeriaStatesAndLGAs` export |

---

### Task 1: Create `locationApi.ts` — API layer with caching

**Files:**
- Create: `components/common/locationApi.ts`

- [ ] **Step 1: Create the API utility file**

```ts
// components/common/locationApi.ts
const BASE_URL = "https://countriesnow.space/api/v0.1";

const cache = new Map<string, string[]>();

export async function fetchStates(country: string): Promise<string[]> {
  const key = `states:${country}`;
  if (cache.has(key)) return cache.get(key)!;

  const response = await fetch(`${BASE_URL}/countries/states`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ country }),
  });

  if (!response.ok) throw new Error("Failed to fetch states");

  const json = await response.json();
  const states: string[] = (json.data?.states || []).map(
    (s: { name: string }) => s.name
  );
  cache.set(key, states);
  return states;
}

export async function fetchCities(
  country: string,
  state: string
): Promise<string[]> {
  const key = `cities:${country}:${state}`;
  if (cache.has(key)) return cache.get(key)!;

  const response = await fetch(`${BASE_URL}/countries/state/cities`, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ country, state }),
  });

  if (!response.ok) throw new Error("Failed to fetch cities");

  const json = await response.json();
  const cities: string[] = json.data || [];
  cache.set(key, cities);
  return cities;
}
```

- [ ] **Step 2: Commit**

```bash
git add components/common/locationApi.ts
git commit -m "feat: add locationApi utility with CountriesNow integration and caching"
```

---

### Task 2: Create `LocationPicker.tsx` — the reusable component

**Files:**
- Create: `components/common/LocationPicker.tsx`
- Read (for styling reference): `components/country/dropdowns.tsx`

- [ ] **Step 1: Create the LocationPicker component**

The component contains an internal `SearchableDropdown` sub-component (not exported) and the main `LocationPicker` that renders 3 instances of it.

```tsx
// components/common/LocationPicker.tsx
import React, { useState, useEffect, useCallback, useRef } from "react";
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  FlatList,
  Modal,
  StyleSheet,
  ActivityIndicator,
} from "react-native";
import MaterialIcons from "@expo/vector-icons/MaterialIcons";
import { COLORS, SIZES } from "../../constants/theme";
import { countries } from "../../constants/api/statesConstants";
import { fetchStates, fetchCities } from "./locationApi";

// --- Types ---

export interface LocationValue {
  country: string;
  state: string;
  city: string;
}

export interface LocationPickerProps {
  value: LocationValue;
  onChange: (location: LocationValue) => void;
  fields?: ("country" | "state" | "city")[];
  required?: ("country" | "state" | "city")[];
  labels?: { country?: string; state?: string; city?: string };
  showLabels?: boolean;
  disabled?: boolean;
  errors?: { country?: string; state?: string; city?: string };
}

// --- Internal SearchableDropdown ---

interface SearchableDropdownProps {
  data: string[];
  selectedItem: string;
  onSelect: (item: string) => void;
  placeholder: string;
  disabled?: boolean;
  loading?: boolean;
  error?: string;
  apiError?: boolean;
  onRetry?: () => void;
  onFallbackTextChange?: (text: string) => void;
}

const SearchableDropdown: React.FC<SearchableDropdownProps> = ({
  data,
  selectedItem,
  onSelect,
  placeholder,
  disabled = false,
  loading = false,
  error,
  apiError = false,
  onRetry,
  onFallbackTextChange,
}) => {
  const [modalVisible, setModalVisible] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const searchInputRef = useRef<TextInput>(null);

  const filteredData = data.filter((item) =>
    item.toLowerCase().includes(searchQuery.toLowerCase())
  );

  const handleSelect = (item: string) => {
    onSelect(item);
    setModalVisible(false);
    setSearchQuery("");
  };

  const displayText = loading
    ? "Loading..."
    : selectedItem || placeholder;

  const isDisabled = disabled || loading;

  // If API failed, show a free-text input as fallback
  if (apiError && !loading) {
    return (
      <View>
        <TextInput
          style={[styles.dropdownTrigger, error ? styles.dropdownError : null]}
          placeholder={placeholder}
          value={selectedItem}
          onChangeText={onFallbackTextChange}
          placeholderTextColor={COLORS.gray}
        />
        {onRetry && (
          <TouchableOpacity onPress={onRetry} style={styles.retryButton}>
            <MaterialIcons name="refresh" size={14} color={COLORS.primary} />
            <Text style={styles.retryText}>Tap to retry</Text>
          </TouchableOpacity>
        )}
        {error && <Text style={styles.errorText}>{error}</Text>}
      </View>
    );
  }

  return (
    <View>
      <TouchableOpacity
        style={[
          styles.dropdownTrigger,
          isDisabled && styles.dropdownDisabled,
          error ? styles.dropdownError : null,
        ]}
        onPress={() => !isDisabled && setModalVisible(true)}
        disabled={isDisabled}
        accessibilityLabel={placeholder}
      >
        <Text
          style={[
            styles.dropdownText,
            !selectedItem && styles.dropdownPlaceholder,
          ]}
        >
          {displayText}
        </Text>
        {loading ? (
          <ActivityIndicator size="small" color={COLORS.gray} />
        ) : (
          <MaterialIcons
            name="arrow-drop-down"
            size={24}
            color={COLORS.gray}
          />
        )}
      </TouchableOpacity>

      {error && <Text style={styles.errorText}>{error}</Text>}

      <Modal
        visible={modalVisible}
        animationType="slide"
        transparent
        onRequestClose={() => setModalVisible(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContent}>
            <View style={styles.modalHeader}>
              <Text style={styles.modalTitle}>{placeholder}</Text>
              <TouchableOpacity onPress={() => setModalVisible(false)}>
                <MaterialIcons name="close" size={24} color={COLORS.dark} />
              </TouchableOpacity>
            </View>

            <View style={styles.searchContainer}>
              <MaterialIcons name="search" size={20} color={COLORS.gray} />
              <TextInput
                ref={searchInputRef}
                style={styles.searchInput}
                placeholder="Search..."
                value={searchQuery}
                onChangeText={setSearchQuery}
                autoFocus
              />
            </View>

            <FlatList
              data={filteredData}
              nestedScrollEnabled
              keyExtractor={(item, index) => `${item}-${index}`}
              keyboardShouldPersistTaps="handled"
              renderItem={({ item }) => (
                <TouchableOpacity
                  style={[
                    styles.listItem,
                    selectedItem === item && styles.selectedListItem,
                  ]}
                  onPress={() => handleSelect(item)}
                >
                  <Text
                    style={[
                      styles.listItemText,
                      selectedItem === item && styles.selectedListItemText,
                    ]}
                  >
                    {item}
                  </Text>
                  {selectedItem === item && (
                    <MaterialIcons
                      name="check"
                      size={20}
                      color={COLORS.primary}
                    />
                  )}
                </TouchableOpacity>
              )}
              ListEmptyComponent={
                <View style={styles.emptyContainer}>
                  <MaterialIcons
                    name="search-off"
                    size={40}
                    color={COLORS.gray}
                  />
                  <Text style={styles.emptyText}>No results found</Text>
                </View>
              }
            />
          </View>
        </View>
      </Modal>
    </View>
  );
};

// --- Main LocationPicker ---

const LocationPicker: React.FC<LocationPickerProps> = ({
  value,
  onChange,
  fields = ["country", "state", "city"],
  required = ["country", "state", "city"],
  labels = {},
  showLabels = true,
  disabled = false,
  errors = {},
}) => {
  const showState = fields.includes("state");
  const showCity = fields.includes("city");
  const [statesList, setStatesList] = useState<string[]>([]);
  const [citiesList, setCitiesList] = useState<string[]>([]);
  const [loadingStates, setLoadingStates] = useState(false);
  const [loadingCities, setLoadingCities] = useState(false);
  const [statesApiError, setStatesApiError] = useState(false);
  const [citiesApiError, setCitiesApiError] = useState(false);

  const initialCountry = useRef(value.country);
  const initialState = useRef(value.state);

  const loadStates = useCallback(async (country: string) => {
    if (!country) {
      setStatesList([]);
      return;
    }
    setLoadingStates(true);
    setStatesApiError(false);
    try {
      const states = await fetchStates(country);
      setStatesList(states);
    } catch {
      setStatesApiError(true);
      setStatesList([]);
    } finally {
      setLoadingStates(false);
    }
  }, []);

  const loadCities = useCallback(
    async (country: string, state: string) => {
      if (!country || !state) {
        setCitiesList([]);
        return;
      }
      setLoadingCities(true);
      setCitiesApiError(false);
      try {
        const cities = await fetchCities(country, state);
        setCitiesList(cities);
      } catch {
        setCitiesApiError(true);
        setCitiesList([]);
      } finally {
        setLoadingCities(false);
      }
    },
    []
  );

  // Load states/cities on mount if pre-filled
  useEffect(() => {
    if (initialCountry.current) {
      loadStates(initialCountry.current);
    }
    if (initialCountry.current && initialState.current) {
      loadCities(initialCountry.current, initialState.current);
    }
  }, [loadStates, loadCities]);

  const handleCountryChange = (country: string) => {
    setStatesList([]);
    setCitiesList([]);
    setStatesApiError(false);
    setCitiesApiError(false);
    onChange({ country, state: "", city: "" });
    if (country) loadStates(country);
  };

  const handleStateChange = (state: string) => {
    setCitiesList([]);
    setCitiesApiError(false);
    onChange({ ...value, state, city: "" });
    if (value.country && state) loadCities(value.country, state);
  };

  const handleCityChange = (city: string) => {
    onChange({ ...value, city });
  };

  const countryLabel = labels.country || "Country";
  const stateLabel = labels.state || "State";
  const cityLabel = labels.city || "City";

  const statePlaceholder = !value.country
    ? "Select country first"
    : `Select ${stateLabel}`;

  const cityPlaceholder = !value.state
    ? "Select state first"
    : `Select ${cityLabel}`;

  return (
    <View style={styles.container}>
      {/* Country */}
      <View style={styles.fieldGroup}>
        {showLabels && (
          <Text style={styles.label}>
            {countryLabel}
            {required.includes("country") ? " *" : ""}
          </Text>
        )}
        <SearchableDropdown
          data={countries}
          selectedItem={value.country}
          onSelect={handleCountryChange}
          placeholder={`Select ${countryLabel}`}
          disabled={disabled}
          error={errors.country}
        />
      </View>

      {/* State */}
      {showState && (
        <View style={styles.fieldGroup}>
          {showLabels && (
            <Text style={styles.label}>
              {stateLabel}
              {required.includes("state") ? " *" : ""}
            </Text>
          )}
          <SearchableDropdown
            data={statesList}
            selectedItem={value.state}
            onSelect={handleStateChange}
            placeholder={statePlaceholder}
            disabled={disabled || !value.country}
            loading={loadingStates}
            error={errors.state}
            apiError={statesApiError}
            onRetry={() => loadStates(value.country)}
            onFallbackTextChange={(text) =>
              onChange({ ...value, state: text, city: "" })
            }
          />
        </View>
      )}

      {/* City */}
      {showCity && (
        <View style={styles.fieldGroup}>
          {showLabels && (
            <Text style={styles.label}>
              {cityLabel}
              {required.includes("city") ? " *" : ""}
            </Text>
          )}
          <SearchableDropdown
            data={citiesList}
            selectedItem={value.city}
            onSelect={handleCityChange}
            placeholder={cityPlaceholder}
            disabled={disabled || !value.state}
            loading={loadingCities}
            error={errors.city}
            apiError={citiesApiError}
            onRetry={() => loadCities(value.country, value.state)}
            onFallbackTextChange={(text) => onChange({ ...value, city: text })}
          />
        </View>
      )}
    </View>
  );
};

// --- Styles ---
// Adopts the bottom-sheet modal pattern from the existing dropdowns.tsx

const styles = StyleSheet.create({
  container: {},
  fieldGroup: {
    marginBottom: 12,
  },
  label: {
    fontSize: 14,
    fontWeight: "500",
    color: COLORS.dark,
    marginBottom: 6,
  },
  dropdownTrigger: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    borderRadius: 12,
    paddingHorizontal: 15,
    height: 56,
    backgroundColor: COLORS.lightGray,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
  },
  dropdownDisabled: {
    opacity: 0.5,
  },
  dropdownError: {
    borderColor: "#ff4444",
    borderWidth: 1.5,
  },
  dropdownText: {
    fontSize: 16,
    color: COLORS.dark,
    flex: 1,
  },
  dropdownPlaceholder: {
    color: COLORS.gray,
  },
  errorText: {
    color: "#ff4444",
    fontSize: 12,
    marginTop: 4,
  },
  retryButton: {
    flexDirection: "row",
    alignItems: "center",
    marginTop: 4,
  },
  retryText: {
    color: COLORS.primary,
    fontSize: 12,
    marginLeft: 4,
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: "rgba(0,0,0,0.5)",
    justifyContent: "flex-end",
  },
  modalContent: {
    backgroundColor: "#fff",
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: "80%",
  },
  modalHeader: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    padding: 20,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  modalTitle: {
    fontSize: 18,
    fontWeight: "600",
    color: COLORS.dark,
  },
  searchContainer: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#f9fafb",
    margin: 20,
    paddingHorizontal: 15,
    borderRadius: 12,
    height: 50,
  },
  searchInput: {
    flex: 1,
    marginLeft: 10,
    fontSize: 16,
  },
  listItem: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    paddingHorizontal: 20,
    paddingVertical: 15,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  selectedListItem: {
    backgroundColor: "rgba(99, 102, 241, 0.1)",
  },
  listItemText: {
    fontSize: 16,
    color: COLORS.dark,
  },
  selectedListItemText: {
    color: COLORS.primary,
    fontWeight: "600",
  },
  emptyContainer: {
    alignItems: "center",
    justifyContent: "center",
    padding: 40,
  },
  emptyText: {
    marginTop: 10,
    color: COLORS.gray,
    fontSize: 16,
  },
});

export default LocationPicker;
```

- [ ] **Step 2: Verify the component renders**

Run: `npx expo start --web`

Open the app and navigate to any screen. No crashes means the new files are syntactically valid and imports resolve. (The component isn't wired in yet — this just checks it compiles.)

- [ ] **Step 3: Commit**

```bash
git add components/common/LocationPicker.tsx
git commit -m "feat: add LocationPicker component with cascading country/state/city dropdowns"
```

---

### Task 3: Migrate Signup screen

**Files:**
- Modify: `components/onboarding/Signup.tsx`

- [ ] **Step 1: Replace CustomPickerWithSearch with LocationPicker**

Changes to `components/onboarding/Signup.tsx`:

1. Replace import:
```ts
// Remove:
import CustomPickerWithSearch from "../country/dropdown";
// Add:
import LocationPicker from "../common/LocationPicker";
```

2. Replace the country picker JSX (around line 174):
```tsx
// Remove:
<CustomPickerWithSearch
  data={countryList}
  selectedItem={country}
  setSelectedItem={setCountry}
  postData={() => {}}
  backgroundColor="#f9f9f9"
/>

// Replace with:
<LocationPicker
  value={{ country, state: "", city: "" }}
  onChange={({ country: c }) => setCountry(c)}
  fields={["country"]}
  required={["country"]}
  showLabels={false}
/>
```

3. Remove unused import:
```ts
// Remove:
import { countries } from "../../constants/api/statesConstants";
```

4. Remove unused state:
```ts
// Remove:
const [countryList] = useState(countries || []);
```

- [ ] **Step 2: Test the signup flow**

Open the app → Signup screen → Verify country dropdown works, search filters correctly, selection persists.

- [ ] **Step 3: Commit**

```bash
git add components/onboarding/Signup.tsx
git commit -m "refactor: migrate Signup to use LocationPicker for country selection"
```

---

### Task 4: Migrate EditProfile screen

**Files:**
- Modify: `components/profile/EditProfile.tsx`

- [ ] **Step 1: Replace native Picker with LocationPicker**

Changes to `components/profile/EditProfile.tsx`:

1. Replace imports:
```ts
// Remove:
import { Picker } from "@react-native-picker/picker";
import { countries } from "../../constants/api/statesConstants";
// Add:
import LocationPicker from "../common/LocationPicker";
```

2. Replace state variables:
```ts
// Remove:
const [country, setCountry] = useState(user.country);
const [countryList, setCountries] = useState(countries);
// Add:
const [location, setLocation] = useState({
  country: user.country || "",
  state: user.state || "",
  city: user.city || "",
});
```

3. Replace the Picker JSX (lines 136-147):
```tsx
// Remove the entire <View style={styles.pickerContainer}> block

// Replace with:
<LocationPicker
  value={location}
  onChange={setLocation}
  required={["country"]}
/>
```

4. Update `handleUpdate` to send all 3 fields:
```ts
// Replace:
formData.append("country", country);
// With:
formData.append("country", location.country);
formData.append("state", location.state);
formData.append("city", location.city);
```

5. Remove unused styles: `pickerContainer`, `picker`

- [ ] **Step 2: Test the profile edit flow**

Open app → Profile → Edit Profile → Verify country/state/city cascade works, pre-fills from user data, update saves correctly.

- [ ] **Step 3: Commit**

```bash
git add components/profile/EditProfile.tsx
git commit -m "refactor: migrate EditProfile to use LocationPicker with full country/state/city"
```

---

### Task 5: Migrate Delivery screen

**Files:**
- Modify: `components/orders/delivery.tsx`

- [ ] **Step 1: Replace 3 dropdowns + inline API calls with LocationPicker**

Changes to `components/orders/delivery.tsx`:

1. Replace imports:
```ts
// Remove:
import { countries } from "../../constants/api/statesConstants";
import PickerWithSearch from "../country/dropdown";
import DropDownState from "../country/statedropdown";
import CityDropDown from "../country/citydropdown";
// Add:
import LocationPicker from "../common/LocationPicker";
```

2. Replace state variables:
```ts
// Remove:
const [location, setLocation] = useState<string | undefined>();
const [states, setStates] = useState<State[]>([]);
const [state, setState] = useState<string | undefined>();
const [countryList] = useState<Country[]>(countries);
const [cities, setCities] = useState<string[]>([]);
const [country, setCountry] = useState<string>("Nigeria");
// Add:
const [locationValue, setLocationValue] = useState({
  country: "Nigeria",
  state: "",
  city: "",
});
```

3. Remove the `postData` and `getCity` functions and their `useEffect`.

4. Remove the `State` type definition. Keep the `Country` type — it's still used by `PhoneNumber` component's `selectedCountry` state.

5. Replace the 3 dropdown JSX blocks (country/state/city pickers + labels) with:
```tsx
<LocationPicker
  value={locationValue}
  onChange={setLocationValue}
/>
```

6. Update the Continue button's validation and params:
```tsx
onPress={() => {
  if (!address || !locationValue.state || !locationValue.city || !locationValue.country || !phoneNumber) {
    return Alert.alert("Error", "Enter every field");
  }
  router.push({
    pathname: "/paymenttype",
    params: {
      address,
      state: locationValue.state,
      city: locationValue.city,
      country: locationValue.country,
      phoneNumber: removeAllSpaces(
        selectedCountry?.callingCode + phoneNumber
      ),
      totalAmount: data,
      fullName,
    },
  });
}}
```

- [ ] **Step 2: Test the delivery flow**

Open app → Cart → Checkout → Delivery → Verify cascading dropdowns, validation, and navigation to payment.

- [ ] **Step 3: Commit**

```bash
git add components/orders/delivery.tsx
git commit -m "refactor: migrate Delivery to use LocationPicker, remove inline API calls"
```

---

### Task 6: Migrate Product Add screen

**Files:**
- Modify: `app/merchants/inventory/add/index.tsx`

- [ ] **Step 1: Replace free-text Location with LocationPicker**

Changes to `app/merchants/inventory/add/index.tsx`:

1. Add import:
```ts
import LocationPicker from "../../../../components/common/LocationPicker";
```

2. Replace state variable:
```ts
// Remove:
const [location, setLocation] = useState('');
// Add:
const [location, setLocation] = useState({ country: "", state: "", city: "" });
```

3. Remove `locationRef` (no longer needed for a TextInput).

4. Replace the Location form group (lines 545-558):
```tsx
// Remove the entire <View style={styles.formGroup}> block containing the Location TextInput

// Replace with:
<View style={styles.formGroup}>
  <LocationPicker
    value={location}
    onChange={setLocation}
    required={["country", "state"]}
  />
</View>
```

5. Update validation (line 202):
```ts
// Remove:
if (!location.trim()) newErrors.location = 'Location is required';
// Replace:
if (!location.country) newErrors.location = 'Country is required';
if (!location.state) newErrors.location = 'State is required';
```

6. Update form submission — concatenate for interim API compatibility (line 245):
```ts
// Remove:
formData.append('location', location);
// Replace:
formData.append('location', [location.city, location.state, location.country].filter(Boolean).join(', '));
```

- [ ] **Step 2: Test product add flow**

Open app → Merchant → Inventory → Add Product → Verify location picker works, validation fires, form submits.

- [ ] **Step 3: Commit**

```bash
git add app/merchants/inventory/add/index.tsx
git commit -m "refactor: migrate product add form to use LocationPicker"
```

---

### Task 7: Migrate Product Edit screen

**Files:**
- Modify: `app/merchants/inventory/edit/[productId].tsx`

- [ ] **Step 1: Apply same changes as Task 6**

The edit screen mirrors the add screen. Apply the same pattern:

1. Add `LocationPicker` import
2. Replace `location` state: parse existing `product.location` string into `{ country, state, city }` on load:
```ts
// When populating from existing product (around line 136):
const parts = (product.location || "").split(", ");
setLocation({
  city: parts.length >= 3 ? parts[0] : "",
  state: parts.length >= 2 ? parts[parts.length - 2] : "",
  country: parts.length >= 1 ? parts[parts.length - 1] : "",
});
```
3. Replace TextInput with `<LocationPicker />`
4. Update validation
5. Update form submission with concatenation

- [ ] **Step 2: Test product edit flow**

Open app → Merchant → Inventory → Edit existing product → Verify pre-fill from existing data, editing works, saves correctly.

- [ ] **Step 3: Commit**

```bash
git add app/merchants/inventory/edit/[productId].tsx
git commit -m "refactor: migrate product edit form to use LocationPicker"
```

---

### Task 8: Migrate Checkout screen — editable shipping address

**Files:**
- Modify: `app/(dashboard)/(tabs)/(home)/checkout/index.tsx`

- [ ] **Step 1: Add LocationPicker import and shipping address state**

```ts
// Add import:
import LocationPicker from "@/components/common/LocationPicker";
```

Add state for editable shipping address and an editing toggle:

```ts
const [editingAddress, setEditingAddress] = useState(false);
const [shippingLocation, setShippingLocation] = useState({
  country: "",
  state: "",
  city: "",
});
```

Initialize from user profile when cart loads (inside the existing `loadCart` callback or a new `useEffect`):

```ts
useEffect(() => {
  setShippingLocation({
    country: currentUser?.country || "",
    state: currentUser?.state || "",
    city: currentUser?.city || "",
  });
}, [currentUser]);
```

- [ ] **Step 2: Update the shippingAddress derivation**

Replace the existing `shippingAddress` constant (lines 97-102):

```ts
// Remove:
const shippingAddress = cartData?.shippingAddress || {
  address: currentUser?.address || '',
  city: currentUser?.city || '',
  state: currentUser?.state || '',
  country: currentUser?.country || '',
};

// Replace with:
const shippingAddress = {
  address: currentUser?.address || '',
  city: shippingLocation.city,
  state: shippingLocation.state,
  country: shippingLocation.country,
};
```

- [ ] **Step 3: Replace the read-only Shipping Address card with editable version**

Replace the Shipping Address section JSX (lines 426-452):

```tsx
{/* Shipping Address */}
<View style={styles.section}>
  <View style={styles.sectionHeader}>
    <View style={styles.sectionTitleContainer}>
      <Ionicons name="location-outline" size={20} color="#8B5A2B" />
      <Text style={styles.sectionTitle}>Shipping Address</Text>
    </View>
    <TouchableOpacity onPress={() => setEditingAddress(!editingAddress)}>
      <Text style={{ color: '#8B5A2B', fontWeight: '600' }}>
        {editingAddress ? 'Done' : 'Edit'}
      </Text>
    </TouchableOpacity>
  </View>
  <View style={styles.card}>
    {editingAddress ? (
      <LocationPicker
        value={shippingLocation}
        onChange={setShippingLocation}
      />
    ) : (
      <>
        <Text style={styles.cardBoldText}>{shippingName}</Text>
        {shippingAddress.address ? (
          <Text style={styles.cardText}>{shippingAddress.address}</Text>
        ) : null}
        <Text style={styles.cardText}>
          {[shippingAddress.city, shippingAddress.state, shippingAddress.country]
            .filter(Boolean)
            .join(', ')}
        </Text>
        {!shippingAddress.country && (
          <TouchableOpacity onPress={() => setEditingAddress(true)}>
            <Text style={{ color: '#8B5A2B', fontWeight: '600', marginTop: 8 }}>
              Add shipping address
            </Text>
          </TouchableOpacity>
        )}
      </>
    )}
  </View>
</View>
```

- [ ] **Step 4: Add address validation before payment**

Update `handleCreateOrders` to validate shipping address before proceeding (at the top of the function):

```ts
const handleCreateOrders = async () => {
  // Validate shipping address
  if (!shippingLocation.country || !shippingLocation.state) {
    Alert.alert('Shipping Address Required', 'Please set your shipping country and state before checking out.');
    setEditingAddress(true);
    return;
  }

  if (!cartData || cartData.cart.items.length === 0) {
    // ... existing check
```

The `buyerCity/buyerState/buyerCountry` variables (lines 118-120) now read from `shippingLocation` instead of `shippingAddress`:

```ts
const buyerCity = shippingLocation.city;
const buyerState = shippingLocation.state;
const buyerCountry = shippingLocation.country;
```

- [ ] **Step 5: Test checkout with editable address**

1. Open checkout → verify address pre-fills from profile
2. Tap "Edit" → verify LocationPicker appears with cascading dropdowns
3. Change country → verify state/city reset
4. Tap "Done" → verify address card updates
5. Try to pay without country/state → verify validation alert
6. Complete a test order → verify correct location is sent to API

- [ ] **Step 6: Commit**

```bash
git add app/(dashboard)/(tabs)/(home)/checkout/index.tsx
git commit -m "feat: make checkout shipping address editable with LocationPicker"
```

---

### Task 9: Delete old dropdown components and clean up statesConstants

**Files:**
- Delete: `components/country/dropdown.tsx`
- Delete: `components/country/dropdowns.tsx`
- Delete: `components/country/statedropdown.tsx`
- Delete: `components/country/citydropdown.tsx`
- Modify: `constants/api/statesConstants.js`

- [ ] **Step 1: Verify no remaining imports of old components**

Run:
```bash
grep -r "country/dropdown\|country/statedropdown\|country/citydropdown\|country/dropdowns" --include="*.tsx" --include="*.ts" --include="*.js" .
```

Expected: No matches (all consumers already migrated).

- [ ] **Step 2: Delete old component files**

```bash
rm components/country/dropdown.tsx
rm components/country/dropdowns.tsx
rm components/country/statedropdown.tsx
rm components/country/citydropdown.tsx
```

- [ ] **Step 3: Remove `nigeriaStatesAndLGAs` from statesConstants.js**

In `constants/api/statesConstants.js`, remove the entire `export const nigeriaStatesAndLGAs = { ... }` block. Keep only the `countries` array export.

- [ ] **Step 4: Verify app compiles**

Run: `npx expo start --web`

Expected: No import errors, app loads.

- [ ] **Step 5: Commit**

```bash
git rm components/country/dropdown.tsx components/country/dropdowns.tsx components/country/statedropdown.tsx components/country/citydropdown.tsx
git add constants/api/statesConstants.js
git commit -m "refactor: delete old dropdown components, remove unused nigeriaStatesAndLGAs"
```

---

### Task 10: Final verification

- [ ] **Step 1: Full smoke test across all migrated screens**

Test each screen that was modified:
1. **Signup** → Country picker works (only country dropdown shown)
2. **Edit Profile** → Country/State/City cascade, saves to API
3. **Delivery** → Location selection, navigation to payment
4. **Add Product** → Location picker, form validation, submit
5. **Edit Product** → Pre-fills existing location, edit + save
6. **Checkout** → Editable address, pre-fills from profile, validates before payment

- [ ] **Step 2: Test edge cases**

1. Select a country → verify states load
2. Change country → verify state/city reset
3. Select state → verify cities load
4. Kill network → verify fallback to free-text input with "Tap to retry"
5. Search with no results → verify "No results found" message
6. Pre-filled values on mount → verify data loads without clearing

- [ ] **Step 3: Commit any fixes from testing**

```bash
git add -A
git commit -m "fix: address issues found during LocationPicker integration testing"
```
