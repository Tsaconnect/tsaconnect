import {
  ScrollView,
  StyleSheet,
  Text,
  View,
  ActivityIndicator,
} from "react-native";
import React, { useState, useEffect } from "react";
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

  return (
    <View style={{ backgroundColor: "#fff", flex: 1, alignItems: "center" }}>
      <ScrollView>
        {loading ? (
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color="#E8A14A" />
          </View>
        ) : services.length > 0 ? (
          services.map((item) => (
            <ServiceCard
              key={item.id || (item as any)._id}
              id={item.id || (item as any)._id}
              title={item.name}
              description={item.description}
              image={item.images?.[0]?.url || (item.images?.[0] as any) || ""}
            />
          ))
        ) : (
          <View style={styles.noResultsContainer}>
            <View style={styles.notFoundContainer}>
              <Text style={styles.notFoundText}>
                No registered available vendor merchant at the moment.
              </Text>
            </View>
          </View>
        )}
      </ScrollView>
      <View
        style={{
          position: "absolute",
          bottom: 5,
          justifyContent: "center",
          alignContent: "center",
        }}
      >
        <Text
          onPress={() => {
            setAppService("Register your service");
            router.push({ pathname: "/serviceaction", params: { index: 1 } });
          }}
          style={{ color: COLORS.primary, fontWeight: "500" }}
        >
          Click here to register as a vendor
        </Text>
      </View>
    </View>
  );
};

export default Services;

const styles = StyleSheet.create({
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    height: "100%",
  },
  noResultsContainer: {
    display: "flex",
    justifyContent: "center",
    alignItems: "center",
    marginTop: 20,
  },
  noResultsText: {
    fontSize: 16,
    color: "#555",
  },
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
  subText: {
    fontSize: 14,
  },
  registerLink: {
    fontSize: 16,
    color: "#E8A14A",
    marginTop: 15,
  },
});
