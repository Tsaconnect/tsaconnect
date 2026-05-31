// app/(productgroup)/productdetails.tsx
//
// Product detail screen — thin route wrapper that reads the product `item` from
// the route params and renders the gallery / info / add-to-cart UI. Restyled
// onto the TSA Signature design system: the bare <View> is now a token-themed
// SafeAreaView shell (light/dark come free from useTheme). The param read
// (useLocalSearchParams) and the conditional render of <ProductDetail item={item} />
// — and therefore all product fetch, variant/qty selection, and add-to-cart
// (AppContext) logic inside it — are preserved verbatim.
import React from "react";
import { useLocalSearchParams } from "expo-router";
import { SafeAreaView } from "react-native-safe-area-context";
import { useTheme } from "@/theme";
import ProductDetail from "../../components/products/productdetail";

const ProductDetails = () => {
  const { colors } = useTheme();
  const item = useLocalSearchParams();

  return (
    <SafeAreaView
      style={{ flex: 1, backgroundColor: colors.background }}
      edges={["top"]}
    >
      {item && <ProductDetail item={item} />}
    </SafeAreaView>
  );
};

export default ProductDetails;
