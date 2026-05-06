import React, { useEffect, useState } from "react";
import { StyleSheet, View } from "react-native";
import { COLORS } from "../../../../constants";
import HeaderSearch from "../../../../components/marketplace/header";
import ProductListCard from "../../../../components/accessories/ProductListCard";
import { api, Category } from "../../../../components/services/api";

const Products = () => {
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
    <View style={styles.container}>
      <HeaderSearch type="Product" />
      <ProductListCard itemList={categories} itemValue="categoryproducts" />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
  },
});

export default Products;
