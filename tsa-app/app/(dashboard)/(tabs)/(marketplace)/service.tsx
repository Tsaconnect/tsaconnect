import { StyleSheet, View } from "react-native";
import React, { useEffect, useState } from "react";
import { COLORS } from "../../../../constants";
import HeaderSearch from "../../../../components/marketplace/header";
import ProductListCard from "../../../../components/accessories/ProductListCard";
import { api, Category } from "../../../../components/services/api";

const Services = () => {
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
    <View style={styles.container}>
      <HeaderSearch type="Service" />
      <ProductListCard itemList={categories} itemValue="categoryservice" />
    </View>
  );
};

export default Services;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
});
