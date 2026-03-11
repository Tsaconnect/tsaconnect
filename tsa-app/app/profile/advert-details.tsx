import React, { useState, useEffect } from "react";
import { StyleSheet, View, Text, ActivityIndicator, Alert } from "react-native";
import { Image } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { useLocalSearchParams, useRoute } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";

const AdvertDetailScreen = () => {
  const { token } = useAuth();
  const [advert, setAdvert] = useState(null);
  const [loading, setLoading] = useState(true);
  const { id } = useLocalSearchParams();

  useEffect(() => {
    fetchAdvert();
  }, []);

  const fetchAdvert = async () => {
    try {
      const response = await axios.get(`${baseUrl}/adverts/${id}`, {
        headers: {
          "Content-Type": "application/json",
          Authorization: `${token}`,
        },
      });
      setAdvert(response.data);
    } catch (error) {
      Alert.alert(
        "Error",
        "Failed to fetch advert details. Please try again later."
      );
    } finally {
      setLoading(false);
    }
  };

  if (loading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color="#9D6B38" />
      </View>
    );
  }

  if (!advert) {
    return (
      <View style={styles.container}>
        <Text>No advert details available.</Text>
      </View>
    );
  }

  return (
    <View style={styles.container}>
      <Image source={{ uri: advert.images[0] }} style={styles.image} />
      <View style={styles.infoContainer}>
        <Text style={styles.title}>{advert.name}</Text>
        <Text style={styles.amount}>{advert.price} USD</Text>
        <Text style={styles.status}>Status: {advert.status}</Text>
        <Text style={styles.description}>{advert.description}</Text>
        <Text style={styles.details}>Location: {advert.location}</Text>
        <Text style={styles.details}>
          Contact: {advert.phoneNumber} / {advert.email}
        </Text>
        <Text style={styles.details}>Stock: {advert.stock}</Text>
        <Text style={styles.details}>
          Negotiable: {advert.negotiable ? "Yes" : "No"}
        </Text>
        <Text style={styles.details}>Company: {advert.companyName}</Text>
        <Text style={styles.details}>
          Created At: {new Date(advert.createdAt).toLocaleDateString()}
        </Text>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f0f0f0",
    padding: 10,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  infoContainer: {
    marginTop: 20,
    padding: 10,
    backgroundColor: "#fff",
    borderRadius: 10,
  },
  title: {
    fontSize: 24,
    fontWeight: "bold",
  },
  amount: {
    fontSize: 20,
    color: "#666",
    marginVertical: 5,
  },
  status: {
    fontSize: 16,
    color: "#666",
    marginVertical: 5,
  },
  description: {
    fontSize: 16,
    color: "#333",
    marginVertical: 10,
  },
  details: {
    fontSize: 14,
    color: "#666",
    marginVertical: 2,
  },
  image: {
    width: "100%",
    height: 200,
    resizeMode: "cover",
    borderRadius: 10,
  },
});

export default AdvertDetailScreen;
