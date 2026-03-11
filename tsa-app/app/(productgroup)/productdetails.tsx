import { StyleSheet, Text, View } from "react-native";
import React from "react";
import { useLocalSearchParams } from "expo-router";
import ProductDetail from "../../components/products/productdetail";

const ProductDetails = () => {
  const item = useLocalSearchParams();

  return <View>{item && <ProductDetail item={item} />}</View>;
};

export default ProductDetails;

const styles = StyleSheet.create({});
