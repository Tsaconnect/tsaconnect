import React, { useState, useEffect } from "react";
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  Image,
  FlatList,
  Alert,
  ActivityIndicator,
} from "react-native";
import { Card, Icon } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { Link, router } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { ADVERT_TYPE_PRODUCT } from "../../constants/constantValues";

const MyAdvertsScreen = () => {
  const { token, setCurrentUser } = useAuth();
  const [adverts, setAdverts] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    if (token) {
      fetchAdverts();
    } else {
      setLoading(false);
    }
  }, [token]);

  const fetchAdverts = async () => {
    try {
      const response = await axios.get(
        `${baseUrl}/products/user`,
        {
          headers: {
            "Content-Type": "application/json",
            Authorization: token,
          },
        }
      );
      setAdverts(response.data.data?.products || []);
    } catch (error: any) {
      if (error?.response?.status === 401) {
        // Token invalid or expired — show empty state silently
        setAdverts([]);
      } else {
        console.error("Error fetching adverts:", error);
        Alert.alert("Error", "Failed to fetch adverts. Please try again later.");
      }
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = (id: any) => {
    Alert.alert(
      "Delete Advert",
      "Are you sure you want to delete this advert?",
      [
        {
          text: "Cancel",
          style: "cancel",
        },
        {
          text: "Delete",
          onPress: async () => {
            try {
              await axios.delete(`${baseUrl}/products/${id}`, {
                headers: {
                  "Content-Type": "application/json",
                  Authorization: `${token}`,
                },
              });
              fetchAdverts();
            } catch (error) {
              console.error("Error deleting advert:", error);
              Alert.alert(
                "Error",
                "Failed to delete advert. Please try again later."
              );
            }
          },
          style: "destructive",
        },
      ],
      { cancelable: true }
    );
  };

  const getFirstImage = (images: any): string | undefined => {
    if (!images) return undefined;
    if (Array.isArray(images)) return images[0];
    try {
      const parsed = typeof images === 'string' ? JSON.parse(images) : images;
      return Array.isArray(parsed) ? parsed[0] : undefined;
    } catch { return undefined; }
  };

  const renderItem = ({ item }: { item: any }) => (
    <Card containerStyle={styles.card}>
      {getFirstImage(item.images) ? (
        <Image
          source={{ uri: getFirstImage(item.images) }}
          style={styles.image}
          resizeMode="contain"
        />
      ) : (
        <View style={[styles.image, { backgroundColor: '#eee', justifyContent: 'center', alignItems: 'center' }]}>
          <Icon name="image" size={40} color="#ccc" />
        </View>
      )}
      <View style={styles.infoContainer}>
        <Text style={styles.title}>{item.name}</Text>
        {item.type === ADVERT_TYPE_PRODUCT && (
          <Text style={styles.amount}>{item.price}</Text>
        )}
        <Text style={[styles.status, item.status === 'Rejected' && styles.statusRejected]}>
          {item.status}
        </Text>
        {item.status === 'Rejected' && (
          <Text style={styles.rejectionReason}>
            Reason: {item.rejectionReason || "Please contact admin for details"}
          </Text>
        )}
        <Text style={styles.status}>{item.type}</Text>
        <Text style={styles.date}>
          {new Date(item.createdAt).toLocaleDateString()}
        </Text>
        <View style={styles.actions}>
          <TouchableOpacity
            onPress={() =>
              router.push({ pathname: `/profile/edit-advert`, params: item })
            }
          >
            <Icon name="edit" size={20} color="#9D6B38" />
          </TouchableOpacity>
          <TouchableOpacity onPress={() => handleDelete(item.id)}>
            <Icon name="delete" size={20} color="#9D6B38" />
          </TouchableOpacity>
          <TouchableOpacity
            onPress={() =>
              router.push({
                pathname: `/profile/advert-details`,
                params: { id: item.id },
              })
            }
          >
            <Icon name="info" size={20} color="#9D6B38" />
          </TouchableOpacity>
        </View>
      </View>
    </Card>
  );

  if (loading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color="#9D6B38" />
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <FlatList
        data={adverts}
        keyExtractor={(item) => item.id.toString()}
        renderItem={renderItem}
        ListEmptyComponent={
          <View style={styles.emptyContainer}>
            <Icon name="inventory" size={72} color="#D4B896" />
            <Text style={styles.emptyTitle}>No adverts yet</Text>
            <Text style={styles.emptySubtitle}>
              Products and services you post will appear here
            </Text>
            <TouchableOpacity
              style={styles.emptyButton}
              onPress={() => {
                router.push({
                  pathname: "/serviceaction",
                  params: { index: 0 },
                });
              }}
              activeOpacity={0.8}
            >
              <Icon name="add" size={20} color="#000" />
              <Text style={styles.emptyButtonText}>Post your first advert</Text>
            </TouchableOpacity>
          </View>
        }
      />
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f0f0f0",
    padding: 10,
  },
  card: {
    borderRadius: 10,
    padding: 20,
    marginVertical: 10,
    backgroundColor: "#F5FCFF",
  },
  image: {
    width: "100%",
    height: 150,
    borderRadius: 10,
  },
  infoContainer: {
    marginTop: 10,
  },
  title: {
    fontSize: 18,
    fontWeight: "bold",
  },
  amount: {
    fontSize: 16,
    color: "#666",
  },
  status: {
    fontSize: 14,
    color: "#666",
  },
  date: {
    fontSize: 12,
    color: "#999",
  },
  actions: {
    flexDirection: "row",
    justifyContent: "space-around",
    marginTop: 10,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  registerLink: {
    fontSize: 16,
    color: "#E8A14A",
    marginTop: 20,
  },
  registerLinkText: {
    marginTop: 15,
    fontSize: 16,
    color: "#E8A14A",
  },
  statusRejected: {
    color: "red",
    fontWeight: "bold",
  },
  rejectionReason: {
    color: "red",
    marginTop: 4,
    fontSize: 12,
    fontStyle: "italic",
  },
  emptyContainer: {
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 80,
    paddingHorizontal: 32,
  },
  emptyTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: '#333',
    marginTop: 16,
  },
  emptySubtitle: {
    fontSize: 14,
    color: '#999',
    textAlign: 'center',
    marginTop: 8,
    lineHeight: 20,
  },
  emptyButton: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFD700',
    paddingHorizontal: 24,
    paddingVertical: 14,
    borderRadius: 12,
    marginTop: 24,
    gap: 8,
  },
  emptyButtonText: {
    fontSize: 16,
    fontWeight: '700',
    color: '#000',
  },
});

export default MyAdvertsScreen;
