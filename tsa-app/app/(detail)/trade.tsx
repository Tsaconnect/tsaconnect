// app/(dashboard)/(tabs)/(home)/trade.tsx
//
// Trade screen (home tab) — route wrapper that renders the Trade & Earn
// experience.
//
// This used to fetch `${baseUrl}/category?type=Service` on mount. That endpoint
// does not exist on the Go API (categories live under /products/category), so
// every visit got a 404 whose body is Gin's plain-text "404 page not found" —
// no `message` field. The catch did `alert(error?.response?.data?.message)`,
// which is React Native's global alert() polyfill, so the screen opened with an
// "Alert / undefined" dialog every single time. The fetched categories were
// never read by anything either — TradeAndEarnScreen takes no props — so the
// whole effect was a dead request with a user-facing failure mode. Removed.
import React from "react";
import { View } from "react-native";
import TradeAndEarnScreen from "@/screens/TradeAndEarnScreen";
import { useTheme } from "@/theme";

const TradeScreen = () => {
  const { colors } = useTheme();
  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <TradeAndEarnScreen />
    </View>
  );
};

export default TradeScreen;
