// Country selection control (onboarding).
//
// Migrated onto the TSA Signature design system. The original implementation was
// a commented-out stub that imported a non-existent `CountryPicker` package
// (`withEmoji` + `withFilter`). This rebuild preserves that exact selection logic
// — a `regionCode` state defaulting to "NG" (a cca2 code) and an `onSelectCountry`
// handler that reads `country.cca2` — and reproduces the picker behaviour on
// @/components/ui primitives instead: `withFilter` → PickerSheet's searchable
// list, `withEmoji` → flag emoji derived from the cca2 code. No legacy
// constants/theme styling and no new dependencies: the country list is sourced
// from the codebase's existing static `countries` constant. Default export and
// the no-prop call shape are unchanged; an optional props interface lets callers
// observe / control the selection without altering the original behaviour.
import React, { useMemo, useState } from "react";
import { View } from "react-native";
import { useTheme } from "@/theme";
import { PickerSheet, SelectRow } from "@/components/ui";
import type { PickerItem } from "@/components/ui";
import { countries } from "../../constants/api/statesConstants";

interface Country {
  cca2: string;
  name: string;
}

interface CountryProps {
  label?: string;
  // Optional initial cca2 region code (preserves the original "NG" default).
  initialRegionCode?: string;
  onSelect?: (country: Country) => void;
}

// cca2 lookup for well-known countries so they render a real flag. Countries
// not in this map use their (unique) full name as the selection key — we
// deliberately do NOT derive a 2-letter code from the name, which would collide
// across countries and show the wrong flag; unmapped countries show no flag.
const CCA2_BY_NAME: Record<string, string> = {
  Nigeria: "NG",
  "United States": "US",
  "United Kingdom": "GB",
  Canada: "CA",
  Ghana: "GH",
  Kenya: "KE",
  "South Africa": "ZA",
  India: "IN",
  China: "CN",
  Germany: "DE",
  France: "FR",
  Brazil: "BR",
  Japan: "JP",
  Australia: "AU",
};

function nameToCca2(name: string): string {
  return CCA2_BY_NAME[name] ?? name;
}

// cca2 → flag emoji (regional-indicator codepoints) for the `withEmoji` behaviour.
function cca2ToFlag(cca2: string): string {
  if (!/^[A-Za-z]{2}$/.test(cca2)) return "";
  const base = 0x1f1e6;
  const upper = cca2.toUpperCase();
  return String.fromCodePoint(
    base + (upper.charCodeAt(0) - 65),
    base + (upper.charCodeAt(1) - 65)
  );
}

export default function Country({
  label,
  initialRegionCode = "NG",
  onSelect,
}: CountryProps = {}) {
  const { spacing } = useTheme();
  const [regionCode, setRegionCode] = useState(initialRegionCode);
  const [sheetVisible, setSheetVisible] = useState(false);

  // Static country list mapped to { cca2, name }; `withFilter`/`withEmoji` come
  // from PickerSheet (searchable) + the flag emoji prefix below.
  const countryList = useMemo<Country[]>(
    () => countries.map((name) => ({ cca2: nameToCca2(name), name })),
    []
  );

  const items = useMemo<PickerItem[]>(
    () =>
      countryList.map((c) => {
        const flag = cca2ToFlag(c.cca2);
        return {
          id: c.cca2,
          label: flag ? `${flag}  ${c.name}` : c.name,
          sublabel: /^[A-Z]{2}$/.test(c.cca2) ? c.cca2 : undefined,
        };
      }),
    [countryList]
  );

  const selected = countryList.find((c) => c.cca2 === regionCode);
  const selectedFlag = selected ? cca2ToFlag(selected.cca2) : "";
  const selectedValue = selected
    ? selectedFlag
      ? `${selectedFlag}  ${selected.name}`
      : selected.name
    : undefined;

  const onSelectCountry = (country: Country) => {
    setRegionCode(country.cca2);
    onSelect?.(country);
  };

  return (
    <View style={{ paddingVertical: spacing.xs }}>
      <SelectRow
        label={label}
        value={selectedValue}
        placeholder="Select country"
        leftIcon="nearMe"
        onPress={() => setSheetVisible(true)}
      />
      <PickerSheet
        visible={sheetVisible}
        onClose={() => setSheetVisible(false)}
        title="Select country"
        items={items}
        selectedId={regionCode}
        searchable
        onSelect={(item) => {
          const match = countryList.find((c) => c.cca2 === item.id);
          if (match) onSelectCountry(match);
          setSheetVisible(false);
        }}
      />
    </View>
  );
}
