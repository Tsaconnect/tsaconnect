import {
  ScrollView,
  StyleSheet,
  Text,
  View,
  ActivityIndicator,
  Pressable,
} from "react-native";
import React, { useState, useEffect } from "react";
import Icon from "react-native-vector-icons/MaterialIcons";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import ServiceCard from "../../components/services/ServiceCard";
import { router, useLocalSearchParams } from "expo-router";
import { useAuth } from "../../AuthContext/AuthContext";
import { COLORS } from "../../constants";
import { api, Product } from "../../components/services/api";

const Services = () => {
  const { value } = useLocalSearchParams<{ value?: string }>();
  const [services, setServices] = useState<Product[]>([]);
  const [loading, setLoading] = useState(true);
  const { setAppService } = useAuth();
  const insets = useSafeAreaInsets();

  useEffect(() => {
    if (!value) {
      setLoading(false);
      return;
    }
    let cancelled = false;
    (async () => {
      const response = await api.getProductsByCategory({
        categoryId: value,
        type: "Service",
      });
      if (cancelled) return;
      if (response.success && Array.isArray(response.data?.products)) {
        setServices(response.data.products);
      } else {
        console.warn("Failed to load services:", response.message);
        setServices([]);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, [value]);

  const handleRegister = () => {
    setAppService("Register your service");
    router.push({ pathname: "/serviceaction", params: { index: 1 } });
  };

  return (
    <View style={styles.container}>
      {loading ? (
        <View style={styles.centered}>
          <ActivityIndicator size="large" color={COLORS.primary} />
        </View>
      ) : services.length === 0 ? (
        <View style={styles.centered}>
          <Icon name="storefront" size={56} color="#E0E0E0" />
          <Text style={styles.emptyTitle}>No vendors yet</Text>
          <Text style={styles.emptyBody}>
            No registered vendor merchants in this category at the moment.
          </Text>
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={[
            styles.scrollContent,
            { paddingBottom: 80 + insets.bottom },
          ]}
        >
          {services.map((item) => (
            <ServiceCard
              key={item.id || (item as any)._id}
              id={item.id || (item as any)._id}
              title={item.name}
              description={item.description}
              image={item.images?.[0]?.url || (item.images?.[0] as any) || ""}
            />
          ))}
        </ScrollView>
      )}

      <View style={[styles.footer, { paddingBottom: 12 + insets.bottom }]}>
        <Pressable onPress={handleRegister} hitSlop={8}>
          <Text style={styles.footerLink}>Click here to register as a vendor</Text>
        </Pressable>
      </View>
    </View>
  );
};

export default Services;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#F5F5F7",
  },
  scrollContent: {
    paddingTop: 8,
  },
  centered: {
    flex: 1,
    alignItems: "center",
    justifyContent: "center",
    paddingHorizontal: 32,
    paddingBottom: 80,
  },
  emptyTitle: {
    fontSize: 16,
    fontWeight: "700",
    color: "#1A1A1A",
    marginTop: 12,
    marginBottom: 4,
  },
  emptyBody: {
    fontSize: 14,
    color: "#666",
    textAlign: "center",
    lineHeight: 20,
  },
  footer: {
    position: "absolute",
    left: 0,
    right: 0,
    bottom: 0,
    paddingTop: 12,
    backgroundColor: "rgba(255,255,255,0.96)",
    borderTopWidth: StyleSheet.hairlineWidth,
    borderTopColor: "#EEE",
    alignItems: "center",
  },
  footerLink: {
    color: COLORS.primary,
    fontWeight: "600",
    fontSize: 14,
  },
});
