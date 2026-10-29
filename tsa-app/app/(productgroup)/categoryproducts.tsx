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
import { router, Stack, useLocalSearchParams } from "expo-router";
import ProductCard from "../../components/designs/productcard";
import { useAuth } from "../../AuthContext/AuthContext";
import { api, Product } from "../../components/services/api";
import { useTheme } from "@/theme";
import { EmptyState, ErrorState, PressableScale, Spinner, Text } from "@/components/ui";
import {
  EMPTY_LISTING_FILTERS,
  isFiltering,
  listingFilterParams,
  ListingFilters,
  type ListingFilterValue,
} from "@/components/forms/ListingFilters";
import { locationLabel } from "@/utils/location";

const Products = () => {
  const { colors, spacing } = useTheme();
  const { setAppService } = useAuth();
  const { value } = useLocalSearchParams<{ value?: string }>();
  const [products, setProducts] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  // The category's own name for the header; the layout's title only knows
  // the category when the user arrived from the category list.
  const [categoryName, setCategoryName] = useState("");
  const [loadFailed, setLoadFailed] = useState(false);
  // Bumped by "Try again" to re-run the load.
  const [attempt, setAttempt] = useState(0);
  // Where, plus the category's listing-field filters (brand, …).
  const [filters, setFilters] = useState<ListingFilterValue>(EMPTY_LISTING_FILTERS);
  const filtersKey = JSON.stringify(listingFilterParams(filters));
  const filtering = isFiltering(filters);

  useEffect(() => {
    if (!value) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoadFailed(false);
    (async () => {
      const response = await api.getProductsByCategory({
        categoryId: value,
        type: "Product",
        ...JSON.parse(filtersKey),
      });
      if (cancelled) return;
      if (response.success && Array.isArray(response.data?.products)) {
        setProducts(response.data.products);
        if (response.data.category?.title) setCategoryName(String(response.data.category.title).trim());
      } else {
        console.warn("Failed to load products:", response.message);
        setProducts([]);
        setLoadFailed(true);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [value, filtersKey, attempt]);

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
        {categoryName ? <Stack.Screen options={{ title: categoryName }} /> : null}
        <ListingFilters categoryId={value} value={filters} onChange={setFilters} />
        {loadFailed ? (
          <View style={{ flex: 1, justifyContent: "center" }}>
            <ErrorState
              title="Couldn't load products"
              subtitle="Check your connection and try again."
              onRetry={() => {
                setLoading(true);
                setAttempt((n) => n + 1);
              }}
            />
          </View>
        ) : products.length > 0 ? (
          <FlatList
            data={products}
            renderItem={({ item }) => (
              <ProductCard
                item={{
                  id: item.id || (item as any)._id,
                  name: item.name,
                  companyName: item.companyName ?? "",
                  location: locationLabel(item as any),
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
                  ? "No products match these filters. Clear one to see more."
                  : "No registered available vendor merchant at the moment."
              }
              actionLabel={filtering ? "Clear filters" : undefined}
              onAction={filtering ? () => setFilters(EMPTY_LISTING_FILTERS) : undefined}
            />
            {registerCta}
          </View>
        )}
      </GestureHandlerRootView>
    </SafeAreaView>
  );
};

export default Products;
