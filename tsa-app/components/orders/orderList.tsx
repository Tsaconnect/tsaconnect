import React, { useEffect, useState } from "react";
import {
  View,
  StyleSheet,
  FlatList,
  ActivityIndicator,
  Text,
} from "react-native";
import { useAuth } from "../../AuthContext/AuthContext";
import Icon from "react-native-vector-icons/MaterialIcons";

import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import OrderCard from "./ordercard";

const OrderList = () => {
  const { setItems, token } = useAuth();
  const [orders, setOrders] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const getItems = async () => {
    try {
      const response = await axios.get(`${baseUrl}/order/my-orders`, {
        headers: {
          Authorization: token,
        },
      });
      const results = response.data?.results ?? response.data?.data ?? [];
      setOrders(Array.isArray(results) ? results : []);
      setItems(results);
    } catch (err: any) {
      console.error("Error fetching orders:", err?.response?.data || err.message);
      if (err?.response?.status === 401) {
        setOrders([]);
      } else {
        setError(err?.response?.data?.message || "Failed to load orders");
      }
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (token) {
      getItems();
    } else {
      setLoading(false);
    }
  }, [token]);

  if (loading) {
    return (
      <View style={styles.centered}>
        <ActivityIndicator size="large" color="#B8860B" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <FlatList
        data={orders}
        keyExtractor={(item) => item.id?.toString()}
        renderItem={({ item }) => <OrderCard order={item} />}
        contentContainerStyle={orders.length === 0 ? styles.centered : styles.listContent}
        ListEmptyComponent={
          <View style={styles.emptyContainer}>
            <Icon name="receipt-long" size={72} color="#D4B896" />
            <Text style={styles.emptyTitle}>
              {error ? "Something went wrong" : "No orders yet"}
            </Text>
            <Text style={styles.emptySubtitle}>
              {error || "Your order history will appear here"}
            </Text>
          </View>
        }
      />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f5f5f5",
  },
  centered: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  listContent: {
    padding: 20,
  },
  emptyContainer: {
    alignItems: "center",
    justifyContent: "center",
    paddingVertical: 80,
    paddingHorizontal: 32,
  },
  emptyTitle: {
    fontSize: 20,
    fontWeight: "700",
    color: "#333",
    marginTop: 16,
  },
  emptySubtitle: {
    fontSize: 14,
    color: "#999",
    textAlign: "center",
    marginTop: 8,
    lineHeight: 20,
  },
});

export default OrderList;
