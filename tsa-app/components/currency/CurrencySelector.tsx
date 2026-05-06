// ──────────────────────────────────────────────
// CurrencySelector — dropdown to switch display currency
// ──────────────────────────────────────────────

import React, { useMemo, useState } from "react";
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  Modal,
  FlatList,
  StyleSheet,
  ActivityIndicator,
  Keyboard,
} from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { useCurrency } from "../../contexts/CurrencyContext";
import { CurrencyConfig } from "../../constants/currencies";

interface CurrencySelectorProps {
  /** Show as a minimal header button vs full-width card. Default: 'header' */
  variant?: "header" | "card";
  /** Optional callback when currency changes */
  onChange?: (currency: CurrencyConfig) => void;
  /** Show the rate info next to each currency */
  showRates?: boolean;
}

export default function CurrencySelector({
  variant = "header",
  onChange,
  showRates = false,
}: CurrencySelectorProps) {
  const { currency, setCurrency, allRates, supportedCurrencies, loading } = useCurrency();
  const [visible, setVisible] = useState(false);

  const handleSelect = async (code: string) => {
    await setCurrency(code);
    const selected = supportedCurrencies.find((c) => c.code === code);
    if (selected && onChange) onChange(selected);
    setVisible(false);
  };

  if (variant === "card") {
    return (
      <View style={styles.cardContainer}>
        <Text style={styles.cardLabel}>Display Currency</Text>
        <TouchableOpacity
          style={styles.cardButton}
          onPress={() => setVisible(true)}
          activeOpacity={0.7}
        >
          <Text style={styles.cardButtonText}>
            {currency.flag ? `${currency.flag} ` : ""}
            {currency.symbol} {currency.code} — {currency.name}
          </Text>
          <Ionicons name="chevron-down" size={18} color="#666" />
        </TouchableOpacity>

        <CurrencyModal
          visible={visible}
          onClose={() => setVisible(false)}
          onSelect={handleSelect}
          selectedCode={currency.code}
          allRates={allRates}
          showRates={showRates}
          currencies={supportedCurrencies}
        />
      </View>
    );
  }

  // Header variant: compact button
  return (
    <>
      <TouchableOpacity
        style={styles.headerButton}
        onPress={() => setVisible(true)}
        activeOpacity={0.7}
      >
        {loading && (
          <ActivityIndicator size="small" color="#D4AF37" style={{ marginRight: 4 }} />
        )}
        <Text style={styles.headerButtonText}>
          {currency.symbol} {currency.code}
        </Text>
        <Ionicons name="chevron-down" size={14} color="#D4AF37" />
      </TouchableOpacity>

      <CurrencyModal
        visible={visible}
        onClose={() => setVisible(false)}
        onSelect={handleSelect}
        selectedCode={currency.code}
        allRates={allRates}
        showRates={showRates}
        currencies={supportedCurrencies}
      />
    </>
  );
}

// ── Modal ──

interface CurrencyModalProps {
  visible: boolean;
  onClose: () => void;
  onSelect: (code: string) => void;
  selectedCode: string;
  allRates: Record<string, any>;
  showRates: boolean;
  currencies: CurrencyConfig[];
}

function CurrencyModal({
  visible,
  onClose,
  onSelect,
  selectedCode,
  allRates,
  showRates,
  currencies,
}: CurrencyModalProps) {
  const [query, setQuery] = useState("");

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return currencies;
    return currencies.filter(
      (c) =>
        c.code.toLowerCase().includes(q) ||
        c.name.toLowerCase().includes(q) ||
        (c.symbol && c.symbol.toLowerCase().includes(q)),
    );
  }, [currencies, query]);

  const handleClose = () => {
    setQuery("");
    Keyboard.dismiss();
    onClose();
  };

  return (
    <Modal
      visible={visible}
      transparent
      animationType="slide"
      onRequestClose={handleClose}
    >
      <View style={styles.modalOverlay}>
        <View style={styles.modalContainer}>
          {/* Header */}
          <View style={styles.modalHeader}>
            <Text style={styles.modalTitle}>Select Currency</Text>
            <TouchableOpacity onPress={handleClose}>
              <Ionicons name="close" size={24} color="#333" />
            </TouchableOpacity>
          </View>

          {/* Search */}
          <View style={styles.searchContainer}>
            <Ionicons name="search" size={18} color="#888" style={styles.searchIcon} />
            <TextInput
              style={styles.searchInput}
              placeholder="Search currencies (e.g. NGN, Naira)"
              placeholderTextColor="#999"
              value={query}
              onChangeText={setQuery}
              autoCorrect={false}
              autoCapitalize="characters"
              clearButtonMode="while-editing"
              returnKeyType="search"
            />
            {query.length > 0 && (
              <TouchableOpacity onPress={() => setQuery("")} style={styles.searchClear}>
                <Ionicons name="close-circle" size={18} color="#bbb" />
              </TouchableOpacity>
            )}
          </View>

          {/* List */}
          <FlatList
            data={filtered}
            keyExtractor={(item) => item.code}
            keyboardShouldPersistTaps="handled"
            ListEmptyComponent={
              <Text style={styles.emptyText}>No currencies match "{query}"</Text>
            }
            renderItem={({ item }) => {
              const isSelected = item.code === selectedCode;
              const rate = allRates[item.code];

              return (
                <TouchableOpacity
                  style={[styles.currencyItem, isSelected && styles.currencyItemSelected]}
                  onPress={() => {
                    setQuery("");
                    onSelect(item.code);
                  }}
                  activeOpacity={0.7}
                >
                  <View style={styles.currencyLeft}>
                    <Text style={styles.currencyFlag}>{item.flag || "💱"}</Text>
                    <View>
                      <Text style={styles.currencyName}>
                        {item.symbol} {item.code}
                      </Text>
                      <Text style={styles.currencyFullName}>{item.name}</Text>
                    </View>
                  </View>

                  <View style={styles.currencyRight}>
                    {rate && showRates && (
                      <Text style={styles.rateText}>
                        1 USD = {rate.midRate?.toFixed(2) || "—"}
                      </Text>
                    )}
                    {isSelected && (
                      <Ionicons name="checkmark-circle" size={22} color="#D4AF37" />
                    )}
                  </View>
                </TouchableOpacity>
              );
            }}
            contentContainerStyle={styles.listContent}
          />
        </View>
      </View>
    </Modal>
  );
}

// ── Styles ──

const styles = StyleSheet.create({
  // Header variant
  headerButton: {
    flexDirection: "row",
    alignItems: "center",
    paddingHorizontal: 10,
    paddingVertical: 6,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: "#D4AF37",
    backgroundColor: "rgba(212, 175, 55, 0.08)",
  },
  headerButtonText: {
    fontSize: 14,
    fontWeight: "700",
    color: "#D4AF37",
    marginRight: 4,
  },

  // Card variant
  cardContainer: {
    paddingHorizontal: 16,
    paddingVertical: 12,
    backgroundColor: "#FFF",
    borderBottomWidth: 1,
    borderBottomColor: "#F0F0F0",
  },
  cardLabel: {
    fontSize: 12,
    fontWeight: "600",
    color: "#888",
    marginBottom: 8,
    textTransform: "uppercase",
    letterSpacing: 0.5,
  },
  cardButton: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    paddingHorizontal: 14,
    paddingVertical: 12,
    borderRadius: 10,
    backgroundColor: "#F8F8F8",
    borderWidth: 1,
    borderColor: "#E8E8E8",
  },
  cardButtonText: {
    fontSize: 15,
    fontWeight: "600",
    color: "#333",
  },

  // Modal
  modalOverlay: {
    flex: 1,
    backgroundColor: "rgba(0,0,0,0.5)",
    justifyContent: "flex-end",
  },
  modalContainer: {
    backgroundColor: "#FFF",
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: "80%",
    paddingBottom: 30,
  },
  modalHeader: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    paddingHorizontal: 20,
    paddingVertical: 16,
    borderBottomWidth: 1,
    borderBottomColor: "#F0F0F0",
  },
  modalTitle: {
    fontSize: 18,
    fontWeight: "700",
    color: "#1A1A1A",
  },

  // Search
  searchContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginHorizontal: 20,
    marginVertical: 12,
    paddingHorizontal: 12,
    paddingVertical: 8,
    backgroundColor: "#F4F4F4",
    borderRadius: 10,
  },
  searchIcon: {
    marginRight: 8,
  },
  searchInput: {
    flex: 1,
    fontSize: 15,
    color: "#1A1A1A",
    padding: 0,
  },
  searchClear: {
    paddingLeft: 6,
  },

  emptyText: {
    textAlign: "center",
    color: "#888",
    fontSize: 14,
    paddingVertical: 30,
  },
  listContent: {
    paddingBottom: 20,
  },
  currencyItem: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    paddingHorizontal: 20,
    paddingVertical: 14,
    borderBottomWidth: StyleSheet.hairlineWidth,
    borderBottomColor: "#F0F0F0",
  },
  currencyItemSelected: {
    backgroundColor: "rgba(212, 175, 55, 0.06)",
  },
  currencyLeft: {
    flexDirection: "row",
    alignItems: "center",
    gap: 12,
  },
  currencyFlag: {
    fontSize: 24,
  },
  currencyName: {
    fontSize: 16,
    fontWeight: "600",
    color: "#1A1A1A",
  },
  currencyFullName: {
    fontSize: 12,
    color: "#888",
    marginTop: 2,
  },
  currencyRight: {
    flexDirection: "row",
    alignItems: "center",
    gap: 8,
  },
  rateText: {
    fontSize: 12,
    color: "#888",
  },
});
