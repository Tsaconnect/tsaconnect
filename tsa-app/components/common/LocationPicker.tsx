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

  // Focus search input after modal animation completes to prevent Android crash
  useEffect(() => {
    if (modalVisible) {
      const timer = setTimeout(() => {
        searchInputRef.current?.focus();
      }, 400);
      return () => clearTimeout(timer);
    }
  }, [modalVisible]);

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
