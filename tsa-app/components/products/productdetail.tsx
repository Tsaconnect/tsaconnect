// components/products/productdetail.tsx
//
// Product detail — image gallery, seller/title/price/rating info, expandable
// description + attributes, verified-customer reviews, and the add-to-cart /
// checkout CTA. Restyled onto the TSA Signature design system (@/theme tokens +
// @/components/ui primitives). Every bit of logic is preserved verbatim: the
// `addItem` POST to `${baseUrl}/cart` (raw axios + Bearer token), `getItems`
// GET on mount, the `checkItemExist` cart-presence check, `cartService.notifyChange()`
// after a direct add, the `images` filter/sort/map, the `isMore` description
// toggle, and the `router.push("/cart")` checkout navigation. The component's
// props interface ({ item }) and default export are unchanged — this is a
// visual / structural migration only.
import React, { useEffect, useState } from "react";
import { View, Image, ScrollView } from "react-native";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { cartService } from "../services/cart";
import { useAuth } from "../../AuthContext/AuthContext";
import { router } from "expo-router";
import { useTheme } from "@/theme";
import {
  AccentCard,
  AmountText,
  Button,
  Card,
  Divider,
  Icon,
  PressableScale,
  Text,
} from "@/components/ui";
import { ListingDetails } from "@/components/forms/ListingDetails";

//@ts-ignore
const ProductDetail = ({ item }) => {
  const { colors, spacing, radius } = useTheme();
  const [isMore, setIsMore] = useState(false);
  const { items, token, setItems } = useAuth();
  console.log(item.attributes);
  //@ts-ignore
  const addItem = async (newItem) => {
    try {
      const response = await axios.post(`${baseUrl}/cart`, newItem, {
        headers: {
          Authorization: `${token}`,
          "Content-Type": "application/json",
        },
      });
      if (response.status === 201) {
        return response.data;
      }
    } catch (error) {
      throw error;
    }
  };
  //@ts-ignore
  const checkItemExist = (name) => {
    return items.some((item) => item.name === name);
  };

  const getItems = async () => {
    try {
      const response = await axios.get(`${baseUrl}/cart`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      if (response.status === 200) {
        setItems(response.data);
      }
    } catch (error) {
      console.error(error);
    }
  };

  useEffect(() => {
    getItems();
  }, []);

  const renderStars = (rating: number) => {
    const stars = [];
    for (let i = 1; i <= 5; i++) {
      stars.push(
        <Icon
          key={i}
          name="star"
          size={16}
          color={i <= rating ? colors.warning : colors.border}
        />
      );
    }
    return stars;
  };

  const images: string[] = Array.isArray(item?.images)
    ? item.images
        .filter((img: any) => img.url && img.url.startsWith("http"))
        .sort((a: any, b: any) => (a.order ?? 0) - (b.order ?? 0))
        .map((img: any) => img.url)
    : [];

  if (!item) return null;

  const inCart = checkItemExist(item.name);

  return (
    <ScrollView
      style={{ flex: 1 }}
      contentContainerStyle={{ paddingBottom: spacing["4xl"] }}
      showsVerticalScrollIndicator={false}
    >
      {/* Gallery */}
      {images.length > 0 && (
        <ScrollView
          horizontal
          pagingEnabled
          showsHorizontalScrollIndicator={false}
          style={{ backgroundColor: colors.surfaceAlt }}
        >
          {images.map((image: string, index: number) => (
            <View
              key={index}
              style={{ alignItems: "center", justifyContent: "center" }}
            >
              <Image
                source={{ uri: image }}
                style={{ width: 360, height: 300, resizeMode: "contain" }}
                accessibilityLabel="Product Image"
              />
            </View>
          ))}
        </ScrollView>
      )}

      <View style={{ padding: spacing.lg, gap: spacing.lg }}>
        {/* Title / location / rating / price */}
        <Card padding="lg">
          <Text variant="title3" weight="700">
            {item.companyName}
            {"'s "}
            {item.name}
          </Text>

          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              flexWrap: "wrap",
              marginTop: spacing.sm,
            }}
          >
            <View
              style={{
                flexDirection: "row",
                alignItems: "center",
                flex: 1,
                gap: spacing.xs,
              }}
            >
              <Icon name="nearMe" size={16} color={colors.textMuted} />
              <Text variant="bodySm" color="textSecondary">
                {item.location}
              </Text>
            </View>
            <View
              style={{
                flexDirection: "row",
                alignItems: "center",
                gap: spacing.xs,
              }}
            >
              <View style={{ flexDirection: "row", gap: 2 }}>
                {renderStars(item.averageRating)}
              </View>
              <Text variant="bodySm" weight="600">
                {item.averageRating}
              </Text>
              <Text variant="caption" color="textMuted">
                {item.reviews?.length || 0} ratings
              </Text>
            </View>
          </View>

          <Divider spacing="md" />

          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
            }}
          >
            <AmountText value={item.price} currency="USD" variant="title1" color="accent" />
            <Text variant="caption" color="textMuted">
              15 items sold
            </Text>
          </View>
        </Card>

        {/* Description */}
        <View>
          <Text variant="title3" weight="700" style={{ marginBottom: spacing.sm }}>
            Description
          </Text>
          <Text variant="bodySm" color="textSecondary">
            {isMore ? item.description : item.description.slice(0, 100)}
          </Text>
          {!isMore && (
            <PressableScale
              onPress={() => setIsMore(true)}
              accessibilityRole="button"
              accessibilityLabel="See more details"
              style={{ marginTop: spacing.sm }}
            >
              <Text variant="bodySm" weight="600" color="accent">
                See more
              </Text>
            </PressableScale>
          )}
        </View>

        {/* The category's listing fields — always shown: they are what buyers filter on. */}
        <ListingDetails
          categoryId={typeof item.category === "string" ? item.category : (item.category as any)?.id}
          values={(item as any).listingValues}
        />

        {/* Attributes (revealed with description) */}
        {isMore && (
          <>
            <Card padding="lg">
              {(Array.isArray(item.attributes) ? item.attributes : []).map(
                (attribute: any, index: number, arr: any[]) => (
                  <View key={index}>
                    <View
                      style={{
                        flexDirection: "row",
                        justifyContent: "space-between",
                        paddingVertical: spacing.xs,
                      }}
                    >
                      <Text variant="bodySm" color="textMuted">
                        {attribute.name}
                      </Text>
                      <Text variant="bodySm" weight="600">
                        {attribute.value}
                      </Text>
                    </View>
                    {index < arr.length - 1 && <Divider />}
                  </View>
                )
              )}
            </Card>
            <PressableScale
              onPress={() => setIsMore(false)}
              accessibilityRole="button"
              accessibilityLabel="View less details"
              style={{ alignSelf: "center" }}
            >
              <Text variant="bodySm" weight="600" color="accent">
                View less
              </Text>
            </PressableScale>
          </>
        )}

        {/* Reviews */}
        <AccentCard padding="lg">
          <Text variant="overline" color="textSecondary">
            VERIFIED CUSTOMER FEEDBACK
          </Text>
          <Text variant="title3" weight="700" style={{ marginTop: spacing.xs }}>
            Product rating and review
          </Text>
          <View
            style={{
              flexDirection: "row",
              alignItems: "baseline",
              gap: spacing.sm,
              marginTop: spacing.xs,
            }}
          >
            <Text variant="bodyMed" weight="700" color="accent">
              {item.averageRating}/5
            </Text>
            <Text variant="caption" color="textMuted">
              {item.reviews?.length || 0} ratings so far
            </Text>
          </View>

          {(Array.isArray(item.reviews) ? item.reviews : []).map(
            (review: any, index: number) => (
              <View key={index} style={{ marginTop: spacing.md }}>
                <Divider spacing="sm" />
                <View
                  style={{
                    flexDirection: "row",
                    justifyContent: "space-between",
                    alignItems: "center",
                    marginBottom: spacing.xs,
                  }}
                >
                  <View style={{ flexDirection: "row", gap: 2 }}>
                    {renderStars(review.rating)}
                  </View>
                  <Text variant="caption" color="textMuted">
                    {review.date}
                  </Text>
                </View>
                <Text variant="bodySm" weight="700">
                  {review.reviewedBy}
                </Text>
                <Text variant="caption" color="textSecondary">
                  {review.reviewText}
                </Text>
              </View>
            )
          )}
        </AccentCard>

        {/* CTA — add to cart / checkout (logic preserved verbatim) */}
        {!inCart ? (
          <Button
            title="Add to cart"
            leftIcon="cart"
            onPress={async () => {
              if (item.name && item.price && images[0]) {
                const newItem = {
                  productId: item.id, // unique id
                  quantity: 1,
                };
                const response = await addItem(newItem);
                await getItems();
                // This add bypasses cartService, so refresh the shared count badge.
                cartService.notifyChange();
              }
            }}
          />
        ) : (
          <Button
            title="Checkout"
            rightIcon="arrowRight"
            onPress={() => {
              router.push("/cart");
            }}
          />
        )}
      </View>
    </ScrollView>
  );
};

export default ProductDetail;
