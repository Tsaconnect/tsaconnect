// app/(dashboard)/(tabs)/(marketplace)/products.tsx
//
// Marketplace — all products list/grid. Restyled onto the TSA Signature design
// system: the legacy COLORS.white container is replaced by a themed
// SafeAreaView (edges top) driven by useTheme tokens so it adapts to light/dark.
// Data fetching (api.getCategoryTree('Product')), state, the effect + cancel
// guard, and the child components that own search + category navigation
// (HeaderSearch, ProductListCard) are unchanged — this is a visual / structural
// migration only.
import React, { useEffect, useState } from "react";
import { SafeAreaView } from "react-native-safe-area-context";
import HeaderSearch from "../../../../components/marketplace/header";
import ProductListCard from "../../../../components/accessories/ProductListCard";
import { api, Category } from "../../../../components/services/api";
import { useTheme } from "@/theme";

const Products = () => {
  const { colors } = useTheme();
  const [categories, setCategories] = useState<Category[]>([]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const response = await api.getCategoryTree("Product");
      if (cancelled) return;
      if (response.success && Array.isArray(response.data)) {
        setCategories(response.data);
      } else {
        console.warn("Failed to load product categories:", response.message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <HeaderSearch type="Product" />
      <ProductListCard itemList={categories} itemValue="categoryproducts" />
    </SafeAreaView>
  );
};

export default Products;
