// app/(dashboard)/myAdverts.tsx
//
// My adverts — the seller's own product/service listings, with edit / delete /
// details actions and the list-your-first-product onboarding empty state.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). Data fetching (axios GET /products/user), the delete flow
// (axios DELETE /products/:id + refetch), 401/session handling, the multi-shape
// response normalization, the image normalization, status/rejection gating, and
// every navigation route + param are preserved verbatim — this is a
// visual / structural migration only.
import React, { useState, useEffect } from "react";
import { View, Image, FlatList, Alert } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { useAuth } from "../../AuthContext/AuthContext";
import { router } from "expo-router";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { ADVERT_TYPE_PRODUCT } from "../../constants/constantValues";
import { useTheme } from "@/theme";
import {
  AmountText,
  Badge,
  Card,
  Divider,
  EmptyState,
  Icon,
  IconButton,
  Spinner,
  Text,
} from "@/components/ui";

// Normalize the auth header to always include the "Bearer " prefix.
function authHeaderFor(token: string | null | undefined): string {
  if (!token) return "";
  return token.startsWith("Bearer ") ? token : `Bearer ${token}`;
}

const MyAdvertsScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const { token } = useAuth();
  const [adverts, setAdverts] = useState<any[]>([]);
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
      const response = await axios.get(`${baseUrl}/products/user`, {
        headers: {
          "Content-Type": "application/json",
          Authorization: authHeaderFor(token),
        },
      });
      const body = response.data;

      // If the API explicitly reports failure with HTTP 200, surface it
      if (body && body.success === false) {
        Alert.alert("Couldn't load products", body.message || "Server reported an error.");
        setAdverts([]);
        return;
      }

      // Response shape can be { data: { products: [] } }, { data: [] }, { products: [] }, or [] directly
      let list: any[] | null = null;
      if (body?.data?.products && Array.isArray(body.data.products)) list = body.data.products;
      else if (body?.products && Array.isArray(body.products)) list = body.products;
      else if (Array.isArray(body?.data)) list = body.data;
      else if (Array.isArray(body)) list = body;

      if (list === null) {
        console.warn("Unexpected /products/user response shape:", body);
        Alert.alert(
          "Couldn't load products",
          "We received an unexpected response from the server. Please try again."
        );
        setAdverts([]);
        return;
      }
      setAdverts(list);
    } catch (error: any) {
      if (error?.response?.status === 401) {
        Alert.alert(
          "Session expired",
          "Please log in again to see your products.",
          [{ text: "OK", onPress: () => router.replace("/login") }]
        );
        setAdverts([]);
      } else {
        console.error("Error fetching products:", error);
        Alert.alert("Error", "Failed to fetch products. Please try again later.");
      }
    } finally {
      setLoading(false);
    }
  };

  const handleDelete = (id: any) => {
    Alert.alert(
      "Delete Product",
      "Are you sure you want to delete this product?",
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
                  Authorization: authHeaderFor(token),
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

  // Normalize a product's images field to the first usable image URL string.
  // Backend stores images as [{id, url, publicId}, ...] but historical data
  // may use plain string URLs or serialized JSON — handle all shapes safely.
  const getFirstImage = (images: any): string | undefined => {
    if (!images) return undefined;
    let arr: any = images;
    if (typeof images === 'string') {
      try { arr = JSON.parse(images); } catch { return undefined; }
    }
    if (!Array.isArray(arr) || arr.length === 0) return undefined;
    const first = arr[0];
    if (typeof first === 'string') return first;
    if (first && typeof first === 'object' && typeof first.url === 'string') return first.url;
    return undefined;
  };

  const renderItem = ({ item }: { item: any }) => {
    const imageUri = getFirstImage(item.images);
    const isRejected = item?.status === 'Rejected';
    // Admin suspension. The listing stays in the seller's own list (so they can
    // see why it was pulled) but is hidden from every buyer, and only an admin
    // can lift it — editing or re-activating it here would be rejected by the API.
    const isSuspended = item?.status === 'suspended';
    const isBlocked = isRejected || isSuspended;
    const blockedReason = isSuspended
      ? item?.suspensionReason || 'Please contact admin for details'
      : item?.rejectionReason || 'Please contact admin for details';
    const formattedDate = (() => {
      const d = new Date(item?.createdAt);
      return isNaN(d.getTime()) ? '' : d.toLocaleDateString();
    })();

    return (
      <Card padding="md" style={{ marginBottom: spacing.md }}>
        {imageUri ? (
          <Image
            source={{ uri: imageUri }}
            style={{ width: '100%', height: 150, borderRadius: radius.md }}
            resizeMode="contain"
          />
        ) : (
          <View
            style={{
              width: '100%',
              height: 150,
              borderRadius: radius.md,
              backgroundColor: colors.surfaceAlt,
              justifyContent: 'center',
              alignItems: 'center',
            }}
          >
            <Icon name="image" size={40} color={colors.textMuted} />
          </View>
        )}

        <View style={{ marginTop: spacing.md }}>
          <Text variant="bodyMed" weight="700" numberOfLines={2}>
            {item?.name ?? 'Untitled'}
          </Text>

          {item?.type === ADVERT_TYPE_PRODUCT && item?.price != null && (
            <AmountText
              value={Number(item.price)}
              currency="USD"
              variant="title3"
              minimumFractionDigits={2}
              maximumFractionDigits={2}
              style={{ marginTop: spacing.xs }}
            />
          )}

          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs, marginTop: spacing.sm }}>
            {item?.status && (
              <Badge
                label={isSuspended ? 'Suspended' : item.status}
                size="sm"
                color={isBlocked ? colors.danger : colors.textSecondary}
                background={isBlocked ? colors.dangerBg : colors.surfaceAlt}
              />
            )}
            {item?.type && <Badge label={item.type} size="sm" />}
          </View>

          {isBlocked && (
            <Text variant="caption" color="danger" style={{ marginTop: spacing.xs, fontStyle: 'italic' }}>
              Reason: {blockedReason}
            </Text>
          )}

          {item?.createdAt && !!formattedDate && (
            <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
              {formattedDate}
            </Text>
          )}

          <Divider spacing="md" />

          <View style={{ flexDirection: 'row', justifyContent: 'space-around' }}>
            <IconButton
              icon="edit"
              accessibilityLabel="Edit product"
              disabled={isSuspended}
              color={isSuspended ? colors.textMuted : colors.accent}
              onPress={() =>
                router.push({
                  pathname: `/profile/edit-advert`,
                  params: { id: item?.id ?? item?._id },
                })
              }
            />
            <IconButton
              icon="trash"
              accessibilityLabel="Delete product"
              color={colors.accent}
              onPress={() => handleDelete(item.id)}
            />
            <IconButton
              icon="info"
              accessibilityLabel="View product details"
              color={colors.accent}
              onPress={() =>
                router.push({
                  pathname: `/profile/advert-details`,
                  params: { id: item.id },
                })
              }
            />
          </View>
        </View>
      </Card>
    );
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <FlatList
        data={adverts}
        keyExtractor={(item, index) => String(item?.id ?? item?._id ?? index)}
        renderItem={renderItem}
        contentContainerStyle={{
          padding: spacing.lg,
          paddingBottom: spacing['4xl'],
          flexGrow: 1,
        }}
        ListEmptyComponent={
          <View style={{ flex: 1, justifyContent: 'center' }}>
            <EmptyState
              icon="storefront"
              title="No products yet"
              subtitle="Products and services you list will appear here"
              actionLabel="List your first product"
              onAction={() =>
                router.push({
                  pathname: "/serviceaction",
                  params: { index: 0 },
                })
              }
            />
          </View>
        }
      />
    </SafeAreaView>
  );
};

export default MyAdvertsScreen;
