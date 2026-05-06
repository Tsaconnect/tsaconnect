import {
  ActivityIndicator,
  FlatList,
  StyleSheet,
  Text,
  View,
} from "react-native";
import React, { useEffect, useState } from "react";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { router, useLocalSearchParams } from "expo-router";
import ProductCard from "../../components/designs/productcard";
import { useAuth } from "../../AuthContext/AuthContext";
import { COLORS } from "../../constants";
import { api, Product } from "../../components/services/api";

const Products = () => {
  const { setAppService } = useAuth();
  const { value } = useLocalSearchParams<{ value?: string }>();
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (!value) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    (async () => {
      const response = await api.getProductsByCategory({
        categoryId: value,
        type: "Product",
      });
      if (cancelled) return;
      if (response.success && Array.isArray(response.data?.products)) {
        setProducts(response.data.products);
      } else {
        console.warn("Failed to load products:", response.message);
        setProducts([]);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [value]);

  if (loading) {
    return (
      <GestureHandlerRootView style={styles.container}>
        <ActivityIndicator size="large" color={COLORS.primary} />
      </GestureHandlerRootView>
    );
  }

  return (
    <GestureHandlerRootView style={styles.container}>
      {products.length > 0 ? (
        <FlatList
          data={products}
          renderItem={({ item }) => (
            <ProductCard
              item={{
                id: item.id || (item as any)._id,
                name: item.name,
                companyName: item.companyName ?? "",
                location: item.location,
                price: item.price,
                averageRating: (item as any).rating?.average ?? 0,
                images: (item.images || [])
                  .map((img: any) => (typeof img === "string" ? img : img?.url))
                  .filter((u: unknown): u is string => typeof u === "string"),
              }}
            />
          )}
          keyExtractor={(item) => item.id || (item as any)._id}
          numColumns={2}
        />
      ) : (
        <View style={styles.notFoundContainer}>
          <Text style={styles.notFoundText}>
            No registered available vendor merchant at the moment.
          </Text>
        </View>
      )}
      <View style={{ position: "absolute", alignItems: "center", bottom: 5 }}>
        <Text
          onPress={() => {
            setAppService("Register your product");
            router.push({ pathname: "/serviceaction", params: { index: 0 } });
          }}
          style={{ color: COLORS.primary }}
        >
          Click here to register as a vendor
        </Text>
      </View>
    </GestureHandlerRootView>
  );
};

export default Products;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    alignItems: "center",
  },
  notFoundContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  notFoundText: {
    fontSize: 14,
  },
});
