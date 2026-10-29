// app/(productgroup)/categoryproducts.tsx
//
// Products in a category — 2-column list/grid of vendor products for the
// selected category. Restyled onto the TSA Signature design system (theme
// tokens + @/components/ui primitives). Data fetching
// (api.getProductsByCategory), the ProductCard item mapping, state, effects,
// and all navigation routes + params (setAppService + /serviceaction) are
// unchanged — this is a visual / structural migration only.
import { FlatList, View } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import React, { useEffect, useState } from "react";
import { GestureHandlerRootView } from "react-native-gesture-handler";
import { router, useLocalSearchParams } from "expo-router";
import ProductCard from "../../components/designs/productcard";
import { useAuth } from "../../AuthContext/AuthContext";
import { api, Product } from "../../components/services/api";
import { useTheme } from "@/theme";
import { EmptyState, PressableScale, Spinner, Text } from "@/components/ui";
import { ListingFilters } from "@/components/forms/ListingFilters";

const Products = () => {
  const { colors, spacing } = useTheme();
  const { setAppService } = useAuth();
  const { value } = useLocalSearchParams<{ value?: string }>();
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  // The category's listing-field filters (brand, …).
  const [filters, setFilters] = useState<Record<string, string>>({});
  const filtersKey = JSON.stringify(filters);
  const filtering = Object.values(filters).some((v) => v !== "");

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
        attr: JSON.parse(filtersKey),
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
  }, [value, filtersKey]);

  const registerCta = (
    <PressableScale
      onPress={() => {
        setAppService("Register your product");
        router.push({ pathname: "/serviceaction", params: { index: 0 } });
      }}
      style={{ alignItems: "center", paddingVertical: spacing.lg }}
    >
      <Text variant="bodySm" weight="600" color="accent" align="center">
        Click here to register as a vendor
      </Text>
    </PressableScale>
  );

  // Full-screen spinner only on the first load: a filter change keeps the
  // filters on screen while the list refreshes.
  if (loading && !filtering && products.length === 0) {
    return (
      <SafeAreaView
        style={{ flex: 1, backgroundColor: colors.background }}
        edges={["top"]}
      >
        <GestureHandlerRootView
          style={{ flex: 1, alignItems: "center", justifyContent: "center" }}
        >
          <Spinner size="large" />
        </GestureHandlerRootView>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView
      style={{ flex: 1, backgroundColor: colors.background }}
      edges={["top"]}
    >
      <GestureHandlerRootView style={{ flex: 1 }}>
        <ListingFilters categoryId={value} value={filters} onChange={setFilters} />
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
            contentContainerStyle={{
              padding: spacing.lg,
              paddingBottom: spacing["4xl"],
            }}
            showsVerticalScrollIndicator={false}
            ListFooterComponent={registerCta}
          />
        ) : (
          <View style={{ flex: 1, justifyContent: "center" }}>
            <EmptyState
              icon="storefront"
              title={filtering ? "No matches" : "No vendors yet"}
              subtitle={
                filtering
                  ? "No products match these filters. Try setting one back to Any."
                  : "No registered available vendor merchant at the moment."
              }
            />
            {registerCta}
          </View>
        )}
      </GestureHandlerRootView>
    </SafeAreaView>
  );
};

export default Products;
