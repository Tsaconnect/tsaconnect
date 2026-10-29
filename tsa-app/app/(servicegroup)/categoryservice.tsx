// app/(servicegroup)/categoryservice.tsx
//
// Services in a category — lists the registered vendor merchants whose service
// products belong to the selected category, plus a "register as a vendor" CTA.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). Data fetching (api.getProductsByCategory), state, effects, the
// cancellation guard, the ServiceCard props, and all navigation routes + params
// are unchanged — this is a visual / structural migration only.
import React, { useState, useEffect } from "react";
import { ScrollView, View } from "react-native";
import { SafeAreaView, useSafeAreaInsets } from "react-native-safe-area-context";
import { router, useLocalSearchParams } from "expo-router";
import ServiceCard from "../../components/services/ServiceCard";
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

const Services = () => {
  const { colors, spacing } = useTheme();
  const { value } = useLocalSearchParams<{ value?: string }>();
  const [services, setServices] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadFailed, setLoadFailed] = useState(false);
  // Bumped by "Try again" to re-run the load.
  const [attempt, setAttempt] = useState(0);
  // Where, plus the category's listing-field filters (institution, gender, …).
  const [filters, setFilters] = useState<ListingFilterValue>(EMPTY_LISTING_FILTERS);
  const filtersKey = JSON.stringify(listingFilterParams(filters));
  const filtering = isFiltering(filters);
  const { setAppService } = useAuth();
  const insets = useSafeAreaInsets();

  useEffect(() => {
    if (!value) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    setLoading(true);
    setLoadFailed(false);
    (async () => {
      const response = await api.getProductsByCategory({
        categoryId: value,
        type: "Service",
        ...JSON.parse(filtersKey),
      });
      if (cancelled) return;
      if (response.success && Array.isArray(response.data?.products)) {
        setServices(response.data.products);
      } else {
        console.warn("Failed to load services:", response.message);
        setServices([]);
        setLoadFailed(true);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [value, filtersKey, attempt]);

  const handleRegister = () => {
    setAppService("Register your service");
    router.push({ pathname: "/serviceaction", params: { index: 1 } });
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <ListingFilters categoryId={value} value={filters} onChange={setFilters} />
      {loading ? (
        <View style={{ flex: 1, alignItems: "center", justifyContent: "center" }}>
          <Spinner size="large" />
        </View>
      ) : loadFailed ? (
        <View style={{ flex: 1, justifyContent: "center", paddingBottom: 80 }}>
          <ErrorState
            title="Couldn't load vendors"
            subtitle="Check your connection and try again."
            onRetry={() => setAttempt((n) => n + 1)}
          />
        </View>
      ) : services.length === 0 ? (
        <View style={{ flex: 1, justifyContent: "center", paddingBottom: 80 }}>
          <EmptyState
            icon="storefront"
            title={filtering ? "No matches" : "No vendors yet"}
            subtitle={
              filtering
                ? "No vendors match these filters. Clear one to see more."
                : "No registered vendor merchants in this category at the moment."
            }
          />
        </View>
      ) : (
        <ScrollView
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{
            paddingTop: spacing.sm,
            paddingBottom: spacing["4xl"] + insets.bottom + 40,
          }}
        >
          {services.map((item) => (
            <ServiceCard
              key={item.id || (item as any)._id}
              id={item.id || (item as any)._id}
              title={item.name}
              description={item.description}
              location={locationLabel(item as any)}
              image={item.images?.[0]?.url || (item.images?.[0] as any) || ""}
            />
          ))}
        </ScrollView>
      )}

      <View
        style={{
          position: "absolute",
          left: 0,
          right: 0,
          bottom: 0,
          paddingTop: spacing.md,
          paddingBottom: spacing.md + insets.bottom,
          backgroundColor: colors.surface,
          borderTopWidth: 1,
          borderTopColor: colors.hairline,
          alignItems: "center",
        }}
      >
        <PressableScale onPress={handleRegister} hitSlop={8}>
          <Text variant="bodySm" weight="600" color="accent">
            Click here to register as a vendor
          </Text>
        </PressableScale>
      </View>
    </SafeAreaView>
  );
};

export default Services;
