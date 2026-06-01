// app/(dashboard)/(tabs)/(home)/trade.tsx
//
// Trade screen (home tab) — route wrapper that fetches Service categories and
// renders the Trade & Earn experience. Migrated onto the TSA Signature design
// system: the legacy COLORS/SIZES constants + hardcoded white background are
// replaced with theme tokens (useTheme → colors.background) so light/dark both
// come free. Data fetching, auth/token gating, state, and the category endpoint
// are preserved verbatim — visual/structural restyle only.
//
// FINANCIAL: no amount/quote/submit logic lives here; the category fetch and its
// auth header are untouched. The caught error is typed (rather than the behavior
// changed) so the pre-existing `error?.response?.data?.message` access resolves.
import React, { useEffect, useState } from "react";
import { View } from "react-native";
import axios from "axios";
import { useAuth } from "../../../../AuthContext/AuthContext";
import { baseUrl } from "../../../../constants/api/apiClient";
import TradeAndEarnScreen from "@/screens/TradeAndEarnScreen";
import { useTheme } from "@/theme";

const TradeScreen = () => {
  const { colors } = useTheme();
  const [categories, setCategories] = useState([]);
  const { token } = useAuth();
  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(`${baseUrl}/category?type=Service`, {
          headers: {
            Authorization: `${token}`,
          },
        });
        const fetchedCategories = response.data.results;
        setCategories(fetchedCategories);
      } catch (error: any) {
        alert(error?.response?.data?.message);
      }
    };
    if (token) {
      fetchCategories();
    }
  }, [token]);
  return (
    <View style={{ flex: 1, backgroundColor: colors.background }}>
      <TradeAndEarnScreen />
    </View>
  );
};

export default TradeScreen;
