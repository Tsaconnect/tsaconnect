import React, { useState, useEffect } from "react";
import { StyleSheet, View, Text, ActivityIndicator, Alert } from "react-native";
import { Image } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { useLocalSearchParams } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";

// Normalize auth header to always include Bearer prefix
function authHeaderFor(token: string | null | undefined): string {
  if (!token) return "";
  return token.startsWith("Bearer ") ? token : `Bearer ${token}`;
}

// Extract first usable image URL from a product's images field.
// Backend shape is [{id, url, publicId}, ...] but accept plain strings or JSON.
function firstImageUrl(images: any): string | undefined {
  if (!images) return undefined;
  let arr: any = images;
  if (typeof images === "string") {
    try { arr = JSON.parse(images); } catch { return undefined; }
  }
  if (!Array.isArray(arr) || arr.length === 0) return undefined;
  const first = arr[0];
  if (typeof first === "string") return first;
  if (first && typeof first === "object" && typeof first.url === "string") return first.url;
  return undefined;
}

const AdvertDetailScreen = () => {
  const { token } = useAuth();
  const [advert, setAdvert] = useState<any | null>(null);
  const [loading, setLoading] = useState(true);
  const { id } = useLocalSearchParams<{ id: string }>();

  useEffect(() => {
    if (id) fetchAdvert();
    else setLoading(false);
  }, [id]);

  const fetchAdvert = async () => {
    try {
      const response = await axios.get(`${baseUrl}/products/${id}`, {
        headers: {
          "Content-Type": "application/json",
          Authorization: authHeaderFor(token),
        },
      });
      // Response shape may be { data: product } or the raw product object.
      const body = response.data;
      const product = body?.data?.id
        ? body.data
        : body?.id
          ? body
          : body?.data?.product ?? null;
      setAdvert(product);
    } catch (error: any) {
      console.error("Fetch product details error:", error);
      Alert.alert(
        "Error",
        "Failed to fetch product details. Please try again later."
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
        <Text>No product details available.</Text>
      </View>
    );
  }

  const imgUrl = firstImageUrl(advert.images);
  const createdDate = advert.createdAt ? (() => {
    const d = new Date(advert.createdAt);
    return isNaN(d.getTime()) ? "" : d.toLocaleDateString();
  })() : "";

  return (
    <View style={styles.container}>
      {imgUrl ? (
        <Image source={{ uri: imgUrl }} style={styles.image} />
      ) : (
        <View style={[styles.image, { backgroundColor: '#eee', justifyContent: 'center', alignItems: 'center' }]}>
          <Text style={{ color: '#999' }}>No image</Text>
        </View>
      )}
      <View style={styles.infoContainer}>
        <Text style={styles.title}>{advert.name ?? 'Untitled'}</Text>
        {advert.price != null && (
          <Text style={styles.amount}>${Number(advert.price).toFixed(2)} USD</Text>
        )}
        {advert.status && <Text style={styles.status}>Status: {advert.status}</Text>}
        {advert.description && <Text style={styles.description}>{advert.description}</Text>}
        {advert.location && <Text style={styles.details}>Location: {advert.location}</Text>}
        {(advert.phoneNumber || advert.email) && (
          <Text style={styles.details}>
            Contact: {[advert.phoneNumber, advert.email].filter(Boolean).join(' / ')}
          </Text>
        )}
        {advert.stock != null && <Text style={styles.details}>Stock: {advert.stock}</Text>}
        {advert.negotiable != null && (
          <Text style={styles.details}>
            Negotiable: {advert.negotiable ? "Yes" : "No"}
          </Text>
        )}
        {advert.companyName && <Text style={styles.details}>Company: {advert.companyName}</Text>}
        {createdDate && <Text style={styles.details}>Created At: {createdDate}</Text>}
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
