import { StyleSheet, View } from "react-native";
import React, { useEffect, useState } from "react";
import { COLORS } from "../../../../constants";
import HeaderSearch from "../../../../components/marketplace/header";
import ProductListCard from "../../../../components/accessories/ProductListCard";
import api from "../../../../components/services/api";

const services = () => {
  const [categories, setCategories] = useState([]);
  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await api.getCategoryTree('Service');
        if (response.success) {
          const raw = response.data || (response as any).results || [];
          setCategories(Array.isArray(raw) ? raw : []);
        }
      } catch (error) {
        console.error('Failed to fetch service categories:', error);
      }
    };
    fetchCategories();
  }, []);
  return (
    <View style={{ flex: 1, backgroundColor: COLORS.white }}>
      <HeaderSearch type="Service"/>
      <ProductListCard itemList={categories} itemValue="categoryservice" />
    </View>
  );
};

export default services;

export const styles = StyleSheet.create({});
