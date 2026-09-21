import React, { useState, useEffect } from "react";
import { View, Alert } from "react-native";
import { Image } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { useLocalSearchParams } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { useTheme } from "@/theme";
import { useCurrency } from "@/contexts/CurrencyContext";
import { Card, Spinner, Text } from "@/components/ui";

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
  const { colors, spacing, radius } = useTheme();
  const { formatPrice } = useCurrency();
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
      <View style={{ flex: 1, justifyContent: "center", alignItems: "center", backgroundColor: colors.background }}>
        <Spinner size="large" />
      </View>
    );
  }

  if (!advert) {
    return (
      <View style={{ flex: 1, padding: spacing.md, backgroundColor: colors.background }}>
        <Text color="textMuted">No product details available.</Text>
      </View>
    );
  }

  const imgUrl = firstImageUrl(advert.images);
  const createdDate = advert.createdAt ? (() => {
    const d = new Date(advert.createdAt);
    return isNaN(d.getTime()) ? "" : d.toLocaleDateString();
  })() : "";

  return (
    <View style={{ flex: 1, backgroundColor: colors.background, padding: spacing.md }}>
      {imgUrl ? (
        <Image source={{ uri: imgUrl }} style={{ width: "100%", height: 200, resizeMode: "cover", borderRadius: radius.lg }} />
      ) : (
        <View
          style={{
            width: "100%",
            height: 200,
            borderRadius: radius.lg,
            backgroundColor: colors.surfaceAlt,
            justifyContent: "center",
            alignItems: "center",
          }}
        >
          <Text color="textMuted">No image</Text>
        </View>
      )}
      <Card padding="lg" style={{ marginTop: spacing.lg }}>
        <Text variant="title2" weight="700">{advert.name ?? 'Untitled'}</Text>
        {advert.price != null && (
          <Text variant="title3" weight="700" color="accent" style={{ marginVertical: spacing.xs }}>
            {formatPrice(Number(advert.price))}
          </Text>
        )}
        {advert.status && (
          <Text variant="bodySm" color="textSecondary" style={{ marginVertical: spacing.xs }}>Status: {advert.status}</Text>
        )}
        {advert.description && (
          <Text variant="bodySm" color="textPrimary" style={{ marginVertical: spacing.sm }}>{advert.description}</Text>
        )}
        {advert.location && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>Location: {advert.location}</Text>
        )}
        {(advert.phoneNumber || advert.email) && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>
            Contact: {[advert.phoneNumber, advert.email].filter(Boolean).join(' / ')}
          </Text>
        )}
        {advert.stock != null && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>Stock: {advert.stock}</Text>
        )}
        {advert.negotiable != null && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>
            Negotiable: {advert.negotiable ? "Yes" : "No"}
          </Text>
        )}
        {advert.companyName && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>Company: {advert.companyName}</Text>
        )}
        {createdDate && (
          <Text variant="caption" color="textMuted" style={{ marginVertical: 2 }}>Created At: {createdDate}</Text>
        )}
      </Card>
    </View>
  );
};

export default AdvertDetailScreen;
