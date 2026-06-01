// ──────────────────────────────────────────────
// CurrencySelector — dropdown to switch display currency
//
// Migrated onto the TSA Signature design system: tokens via useTheme()
// (colors/spacing/radius) and primitives from @/components/ui (Text, Icon,
// Input, Sheet, Spinner, PressableScale, EmptyState). No legacy COLORS/SIZES/
// FONTS/SHADOWS, no hardcoded hex/magic numbers. All logic — the useCurrency
// hook usage, controlled/uncontrolled selection, search/filter, rate display,
// and modal selection behavior — is preserved verbatim. The props interface and
// default export are unchanged (HEAVILY REUSED across P2P, settings, and
// payment-method screens).
// ──────────────────────────────────────────────

import React, { useMemo, useState } from "react";
import { View, FlatList, Keyboard } from "react-native";
import { useCurrency } from "../../contexts/CurrencyContext";
import { CurrencyConfig } from "../../constants/currencies";
import { useTheme } from "@/theme";
import {
  Text,
  Icon,
  Input,
  Sheet,
  Spinner,
  EmptyState,
  PressableScale,
} from "@/components/ui";

interface CurrencySelectorProps {
  /** Show as a minimal header button vs full-width card. Default: 'header' */
  variant?: "header" | "card";
  /** Optional callback when currency changes */
  onChange?: (currency: CurrencyConfig) => void;
  /** Show the rate info next to each currency */
  showRates?: boolean;
  /** Controlled mode: reflect this code and report changes via onSelectCode
   *  instead of mutating the global app currency. Used by the P2P market
   *  selector so switching trading currency doesn't change global display. */
  selectedCode?: string;
  onSelectCode?: (code: string) => void;
}

export default function CurrencySelector({
  variant = "header",
  onChange,
  showRates = false,
  selectedCode,
  onSelectCode,
}: CurrencySelectorProps) {
  const { colors, spacing, radius } = useTheme();
  const { currency, setCurrency, allRates, supportedCurrencies, loading } = useCurrency();
  const [visible, setVisible] = useState(false);

  const display =
    selectedCode != null
      ? supportedCurrencies.find((c) => c.code === selectedCode) ?? {
          code: selectedCode,
          symbol: selectedCode,
          name: selectedCode,
        }
      : currency;

  const handleSelect = async (code: string) => {
    if (onSelectCode) {
      onSelectCode(code);
    } else {
      await setCurrency(code);
    }
    const selected = supportedCurrencies.find((c) => c.code === code);
    if (selected && onChange) onChange(selected);
    setVisible(false);
  };

  if (variant === "card") {
    return (
      <View
        style={{
          paddingHorizontal: spacing.lg,
          paddingVertical: spacing.md,
          backgroundColor: colors.surface,
          borderBottomWidth: 1,
          borderBottomColor: colors.hairline,
        }}
      >
        <Text
          variant="overline"
          color="textMuted"
          style={{ marginBottom: spacing.sm }}
        >
          DISPLAY CURRENCY
        </Text>
        <PressableScale
          onPress={() => setVisible(true)}
          accessibilityRole="button"
          style={{
            flexDirection: "row",
            alignItems: "center",
            justifyContent: "space-between",
            gap: spacing.sm,
            minHeight: 48,
            paddingHorizontal: spacing.md,
            borderRadius: radius.md,
            borderWidth: 1.5,
            borderColor: colors.border,
            backgroundColor: colors.surfaceAlt,
          }}
        >
          <Text variant="body" weight="600" style={{ flex: 1 }} numberOfLines={1}>
            {display.flag ? `${display.flag} ` : ""}
            {display.symbol} {display.code} — {display.name}
          </Text>
          <Icon name="chevronDown" size={18} color={colors.textMuted} />
        </PressableScale>

        <CurrencyModal
          visible={visible}
          onClose={() => setVisible(false)}
          onSelect={handleSelect}
          selectedCode={selectedCode ?? currency.code}
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
      <PressableScale
        onPress={() => setVisible(true)}
        accessibilityRole="button"
        style={{
          flexDirection: "row",
          alignItems: "center",
          gap: spacing.xs,
          paddingHorizontal: spacing.md,
          paddingVertical: spacing.xs,
          borderRadius: radius.sm,
          borderWidth: 1,
          borderColor: colors.accent,
          backgroundColor: colors.accentMuted,
        }}
      >
        {loading && (
          <View style={{ marginRight: spacing.xs }}>
            <Spinner size="small" />
          </View>
        )}
        <Text variant="bodySm" weight="700" color="accent">
          {display.symbol} {display.code}
        </Text>
        <Icon name="chevronDown" size={14} color={colors.accent} />
      </PressableScale>

      <CurrencyModal
        visible={visible}
        onClose={() => setVisible(false)}
        onSelect={handleSelect}
        selectedCode={selectedCode ?? currency.code}
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
  const { colors, spacing } = useTheme();
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
    <Sheet visible={visible} onClose={handleClose} title="Select Currency">
      {/* Search */}
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
        <Input
          placeholder="Search currencies (e.g. NGN, Naira)"
          value={query}
          onChangeText={setQuery}
          autoCorrect={false}
          autoCapitalize="characters"
          clearButtonMode="while-editing"
          returnKeyType="search"
          leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
          rightAdornment={
            query.length > 0 ? (
              <PressableScale onPress={() => setQuery("")} accessibilityLabel="Clear search">
                <Icon name="closeCircle" size={18} color={colors.textMuted} />
              </PressableScale>
            ) : undefined
          }
        />
      </View>

      {/* List */}
      <FlatList
        data={filtered}
        keyExtractor={(item) => item.code}
        keyboardShouldPersistTaps="handled"
        style={{ flexShrink: 1 }}
        ListEmptyComponent={
          <EmptyState icon="search" title={`No currencies match "${query}"`} />
        }
        renderItem={({ item }) => {
          const isSelected = item.code === selectedCode;
          const rate = allRates[item.code];

          return (
            <PressableScale
              onPress={() => {
                setQuery("");
                onSelect(item.code);
              }}
              accessibilityRole="button"
              accessibilityState={{ selected: isSelected }}
              style={{
                flexDirection: "row",
                alignItems: "center",
                justifyContent: "space-between",
                paddingHorizontal: spacing.lg,
                paddingVertical: spacing.md,
                backgroundColor: isSelected ? colors.accentMuted : "transparent",
              }}
            >
              <View style={{ flexDirection: "row", alignItems: "center", gap: spacing.md, flex: 1 }}>
                <Text variant="title2">{item.flag || "💱"}</Text>
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="600">
                    {item.symbol} {item.code}
                  </Text>
                  <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs / 2 }}>
                    {item.name}
                  </Text>
                </View>
              </View>

              <View style={{ flexDirection: "row", alignItems: "center", gap: spacing.sm }}>
                {rate && showRates && (
                  <Text variant="caption" color="textMuted" numeric>
                    1 USD = {rate.midRate?.toFixed(2) || "—"}
                  </Text>
                )}
                {isSelected && <Icon name="checkCircle" size={22} color={colors.accent} />}
              </View>
            </PressableScale>
          );
        }}
        contentContainerStyle={{ paddingTop: spacing.sm, paddingBottom: spacing.lg }}
      />
    </Sheet>
  );
}
