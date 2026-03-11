import { StyleSheet, View, Text, TextInput } from "react-native";
import React from "react";
import { COLORS } from "../../../../constants";
import MarketplaceScreen from "@/screens/marketplace";


const digital = () => {
  return (
    <View style={{ flex: 1, backgroundColor: COLORS.white }}>
      <MarketplaceScreen />
    </View>
  );
};

export default digital;

export const styles = StyleSheet.create({});
