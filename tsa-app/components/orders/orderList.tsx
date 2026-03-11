import React, { useEffect, useState } from "react";
import {
  View,
  StyleSheet,
  FlatList,
  ActivityIndicator,
  Text,
} from "react-native";
import { useAuth } from "../../AuthContext/AuthContext";

import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import OrderCard from "./ordercard";

const OrderList = () => {
  const { setItems, token } = useAuth();
  const [orders, setOrders] = useState();
  const [loading, setLoading] = useState(true);
  const getItems = async () => {
    try {
      const response = await axios.get(`${baseUrl}/order/my-orders`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      setItems(response.data.results);
      if (response.status === 200) {
        const products = response.data.results.find(
          (item: any) => item.product
        );
        console.log(products.product); // use this list to iterate and get products for a user ID
        setOrders(response.data.results);
        return;
      } else {
        return;
      }
    } catch (error) {
      return;
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    getItems();
  }, []);

  if (loading) {
    return (
      <View style={styles.container}>
        <ActivityIndicator size="large" color="#0000ff" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      {
        //@ts-ignore
        orders?.length == 0 ? (
          <Text style={styles.noOrdersText}>You don't have any orders</Text>
        ) : (
          <FlatList
            data={orders}
            keyExtractor={(item) => item.id}
            renderItem={({ item }) => <OrderCard order={item} />}
            contentContainerStyle={styles.container}
          />
        )
      }
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    /*  flex: 1, */
    padding: 20,
    backgroundColor: "#f5f5f5",
  },
  list: {
    paddingBottom: 20,
  },
  itemContainer: {
    flexDirection: "row",
    backgroundColor: "#fff",
    padding: 10,
    marginBottom: 10,
    borderRadius: 5,
    alignItems: "center",
  },
  image: {
    width: 50,
    height: 50,
    marginRight: 10,
    borderRadius: 5,
  },
  details: {
    flex: 1,
  },
  name: {
    fontSize: 16,
    fontWeight: "bold",
  },
  price: {
    fontSize: 14,
    color: "#888",
    marginVertical: 5,
  },
  quantityContainer: {
    flexDirection: "row",
    alignItems: "center",
  },
  button: {
    width: 30,
    height: 30,
    borderRadius: 15,
    backgroundColor: "#ddd",
    alignItems: "center",
    justifyContent: "center",
  },
  buttonText: {
    fontSize: 20,
    fontWeight: "bold",
  },
  quantity: {
    marginHorizontal: 10,
    fontSize: 16,
  },
  totalContainer: {
    paddingVertical: 10,
    borderTopWidth: 1,
    borderColor: "#ddd",
  },
  checkoutContainer: {
    paddingVertical: 10,
    borderTopWidth: 1,
    marginBottom: 20,
  },
  totalText: {
    fontSize: 18,
    fontWeight: "bold",
    textAlign: "center",
  },
  removeButton: {
    backgroundColor: "#ff4444",
    padding: 5,
    borderRadius: 5,
  },
  removeButtonText: {
    color: "#fff",
    fontWeight: "bold",
  },
  noOrdersText: {
    fontSize: 18,
    color: "#888",
    textAlign: "center",
  },
});

export default OrderList;
