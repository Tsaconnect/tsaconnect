// app/(dashboard)/(tabs)/(marketplace)/digital.tsx
//
// Marketplace — digital products tab. Thin wrapper that renders the shared
// MarketplaceScreen. Migrated onto the TSA Signature design system: the legacy
// constants `COLORS` import and hardcoded white background are replaced with
// theme tokens via useTheme(), so light/dark both come free. The wrapped
// MarketplaceScreen (data fetching, state, navigation) is rendered verbatim.
import { StyleSheet, View } from "react-native";
import React from "react";
import { useTheme } from "@/theme";
import MarketplaceScreen from "@/screens/marketplace";

const DigitalScreen = () => {
  const { colors } = useTheme();
  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <MarketplaceScreen />
    </View>
  );
};

export default DigitalScreen;

export const styles = StyleSheet.create({});
