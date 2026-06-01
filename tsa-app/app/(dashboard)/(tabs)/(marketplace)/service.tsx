// app/(dashboard)/(tabs)/(marketplace)/service.tsx
//
// Marketplace — service category browse list. Restyled onto the TSA Signature
// design system: the legacy COLORS.white container is replaced by a themed
// SafeAreaView (edges top) driven by useTheme tokens so it adapts to light/dark.
// Data fetching (api.getCategoryTree('Service')), state, the effect + cancel
// guard, and the child components that own search + category navigation
// (HeaderSearch, ProductListCard) are unchanged — this is a visual / structural
// migration only.
import React, { useEffect, useState } from "react";
import { SafeAreaView } from "react-native-safe-area-context";
import HeaderSearch from "../../../../components/marketplace/header";
import ProductListCard from "../../../../components/accessories/ProductListCard";
import { api, Category } from "../../../../components/services/api";
import { useTheme } from "@/theme";

const Services = () => {
  const { colors } = useTheme();
  const [categories, setCategories] = useState<Category[]>([]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      const response = await api.getCategoryTree("Service");
      if (cancelled) return;
      if (response.success && Array.isArray(response.data)) {
        setCategories(response.data);
      } else {
        console.warn("Failed to load service categories:", response.message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <HeaderSearch type="Service" />
      <ProductListCard itemList={categories} itemValue="categoryservice" />
    </SafeAreaView>
  );
};

export default Services;
