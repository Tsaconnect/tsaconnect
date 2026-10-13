// components/common/LocationPicker.tsx
//
// Cascading country → state → city selection control. Country list is static
// (constants/api/statesConstants); states/cities are fetched on demand from
// locationApi (countriesnow.space) with an in-memory cache. When the remote API
// fails for states/cities, the affected field degrades to a free-text input so
// the user is never blocked, plus a retry affordance.
//
// Migrated onto the TSA Signature design system: selection rows use SelectRow,
// the searchable picker modal uses PickerSheet, and the API-failure fallback uses
// the Input primitive — all driven by @/theme tokens (no legacy COLORS/SIZES).
// Selection logic, props interface, and exports are preserved verbatim.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Icon, Input, PickerSheet, PressableScale, SelectRow, Text } from '@/components/ui';
import { countries } from '../../constants/api/statesConstants';
import { fetchStates, fetchCities } from './locationApi';

// --- Types ---

export interface LocationValue {
  country: string;
  state: string;
  city: string;
}

export interface LocationPickerProps {
  value: LocationValue;
  onChange: (location: LocationValue) => void;
  fields?: ('country' | 'state' | 'city')[];
  required?: ('country' | 'state' | 'city')[];
  labels?: { country?: string; state?: string; city?: string };
  showLabels?: boolean;
  disabled?: boolean;
  errors?: { country?: string; state?: string; city?: string };
}

// --- Internal SearchableDropdown ---

interface SearchableDropdownProps {
  label?: string;
  data: string[];
  selectedItem: string;
  onSelect: (item: string) => void;
  placeholder: string;
  disabled?: boolean;
  loading?: boolean;
  error?: string;
  apiError?: boolean;
  /** No list for this choice (the lookup has none): type it instead, no retry. */
  freeText?: boolean;
  onRetry?: () => void;
  onFallbackTextChange?: (text: string) => void;
}

const SearchableDropdown: React.FC<SearchableDropdownProps> = ({
  label,
  data,
  selectedItem,
  onSelect,
  placeholder,
  disabled = false,
  loading = false,
  error,
  apiError = false,
  freeText = false,
  onRetry,
  onFallbackTextChange,
}) => {
  const { colors, spacing } = useTheme();
  const [sheetVisible, setSheetVisible] = useState(false);

  const handleSelect = (item: string) => {
    onSelect(item);
    setSheetVisible(false);
  };

  const displayValue = loading ? 'Loading…' : selectedItem;

  const isDisabled = disabled || loading;

  // If the remote API failed, degrade to a free-text input so the user can still
  // enter a value, plus a retry affordance.
  if ((apiError || freeText) && !loading) {
    return (
      <View>
        <Input
          label={label}
          placeholder={placeholder}
          value={selectedItem}
          onChangeText={onFallbackTextChange}
          error={error}
        />
        {apiError && onRetry && (
          <PressableScale
            onPress={onRetry}
            style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.xs }}
          >
            <Icon name="refresh" size={14} color={colors.accent} />
            <Text variant="caption" color="accent">
              Tap to retry
            </Text>
          </PressableScale>
        )}
      </View>
    );
  }

  return (
    <View>
      <SelectRow
        label={label}
        value={displayValue}
        placeholder={placeholder}
        onPress={() => setSheetVisible(true)}
        disabled={isDisabled}
        error={error}
      />

      <PickerSheet
        visible={sheetVisible}
        onClose={() => setSheetVisible(false)}
        title={placeholder}
        items={data.map((item) => ({ id: item, label: item }))}
        selectedId={selectedItem || undefined}
        onSelect={(item) => handleSelect(item.id)}
        searchable
        emptyText="No results found"
      />
    </View>
  );
};

// --- Main LocationPicker ---

const LocationPicker: React.FC<LocationPickerProps> = ({
  value,
  onChange,
  fields = ['country', 'state', 'city'],
  required = ['country', 'state', 'city'],
  labels = {},
  showLabels = true,
  disabled = false,
  errors = {},
}) => {
  const { spacing } = useTheme();
  const showState = fields.includes('state');
  const showCity = fields.includes('city');
  const [statesList, setStatesList] = useState<string[]>([]);
  const [citiesList, setCitiesList] = useState<string[]>([]);
  const [loadingStates, setLoadingStates] = useState(false);
  const [loadingCities, setLoadingCities] = useState(false);
  const [statesApiError, setStatesApiError] = useState(false);
  const [citiesApiError, setCitiesApiError] = useState(false);
  const [statesEmpty, setStatesEmpty] = useState(false);
  const [citiesEmpty, setCitiesEmpty] = useState(false);

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
      setStatesEmpty(states.length === 0);
    } catch {
      setStatesApiError(true);
      setStatesEmpty(false);
      setStatesList([]);
    } finally {
      setLoadingStates(false);
    }
  }, []);

  const loadCities = useCallback(async (country: string, state: string) => {
    if (!country || !state) {
      setCitiesList([]);
      return;
    }
    setLoadingCities(true);
    setCitiesApiError(false);
    try {
      const cities = await fetchCities(country, state);
      setCitiesList(cities);
      setCitiesEmpty(cities.length === 0);
    } catch {
      setCitiesApiError(true);
      setCitiesEmpty(false);
      setCitiesList([]);
    } finally {
      setLoadingCities(false);
    }
  }, []);

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
    setStatesEmpty(false);
    setCitiesEmpty(false);
    onChange({ country, state: '', city: '' });
    if (country) loadStates(country);
  };

  const handleStateChange = (state: string) => {
    setCitiesList([]);
    setCitiesApiError(false);
    setCitiesEmpty(false);
    onChange({ ...value, state, city: '' });
    if (value.country && state) loadCities(value.country, state);
  };

  const handleCityChange = (city: string) => {
    onChange({ ...value, city });
  };

  const countryLabel = labels.country || 'Country';
  const stateLabel = labels.state || 'State';
  const cityLabel = labels.city || 'City';

  const statePlaceholder = !value.country ? 'Select country first' : `Select ${stateLabel}`;

  const cityPlaceholder = !value.state ? 'Select state first' : `Select ${cityLabel}`;

  return (
    <View>
      {/* Country */}
      <View style={{ marginBottom: spacing.md }}>
        <SearchableDropdown
          label={showLabels ? `${countryLabel}${required.includes('country') ? ' *' : ''}` : undefined}
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
        <View style={{ marginBottom: spacing.md }}>
          <SearchableDropdown
            label={showLabels ? `${stateLabel}${required.includes('state') ? ' *' : ''}` : undefined}
            data={statesList}
            selectedItem={value.state}
            onSelect={handleStateChange}
            placeholder={statePlaceholder}
            disabled={disabled || !value.country}
            loading={loadingStates}
            error={errors.state}
            apiError={statesApiError}
            freeText={statesEmpty}
            onRetry={() => loadStates(value.country)}
            onFallbackTextChange={(text) => onChange({ ...value, state: text, city: '' })}
          />
        </View>
      )}

      {/* City */}
      {showCity && (
        <View style={{ marginBottom: spacing.md }}>
          <SearchableDropdown
            label={showLabels ? `${cityLabel}${required.includes('city') ? ' *' : ''}` : undefined}
            data={citiesList}
            selectedItem={value.city}
            onSelect={handleCityChange}
            placeholder={cityPlaceholder}
            disabled={disabled || !value.state}
            loading={loadingCities}
            error={errors.city}
            apiError={citiesApiError}
            freeText={citiesEmpty}
            onRetry={() => loadCities(value.country, value.state)}
            onFallbackTextChange={(text) => onChange({ ...value, city: text })}
          />
        </View>
      )}
    </View>
  );
};

export default LocationPicker;
