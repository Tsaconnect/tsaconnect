// components/designs/productcard.tsx
//
// Legacy designs product card (half-width grid tile) used by the designs
// gallery / home rows. Migrated onto the TSA Signature design system (theme
// tokens via useTheme() + @/components/ui primitives + the already-migrated
// designs/Button.tsx + designs/SubInfo.tsx children). The props interface,
// exports, the useAuth hook order, the cart add/exist/fetch logic, the
// useEffect, navigation route + params (`/productdetails` with the item),
// cartService.notifyChange(), and the Add-to-cart / Checkout conditional are
// all preserved verbatim — this is a visual / structural restyle only.
import { View, Image, Dimensions } from "react-native";
import React, { useEffect } from "react";
import { assets } from "../../constants";
import { CircleButton, RatingButton, RectButton, SellerButton } from "./Button";
import { Catalogue, ProductTitle } from "./SubInfo";
import { router } from "expo-router";
import { useAuth } from "../../AuthContext/AuthContext";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { cartService } from "../services/cart";
import { useTheme } from "@/theme";
import { Card, Icon, PressableScale, Text } from "@/components/ui";

type ProductCardProps = {
  item: {
    id: string;
    name: string;
    companyName: string;
    location: string;
    price: number;
    averageRating: number;
    images: string[];
  };
};

const ProductCard: React.FC<ProductCardProps> = ({ item }) => {
  const { items, token, setItems } = useAuth();
  const { colors, spacing, radius } = useTheme();
  const cardWidth = Dimensions.get("window").width / 2 - 15;

  const addItem = async (newItem: { productId: string; quantity: number }) => {
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

  const checkItemExist = (name: string) => {
    return items.some((existingItem) => existingItem.name === name);
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
      console.error("Failed to fetch items:", error);
    }
  };

  useEffect(() => {
    getItems();
  }, []);

  return (
    <Card
      style={{
        padding: 0,
        borderTopEndRadius: radius.md,
        marginBottom: spacing.sm,
        marginHorizontal: 5,
        width: cardWidth,
        overflow: "hidden",
      }}
    >
      <View style={{ width: "100%", height: 150 }}>
        <PressableScale
          onPress={() => {
            router.push({
              pathname: "/productdetails",
              params: item,
            });
          }}
        >
          <Image
            source={{ uri: item.images[0] }}
            resizeMode="cover"
            style={{
              width: "100%",
              height: "100%",
              borderTopLeftRadius: radius.md,
              borderTopRightRadius: radius.md,
            }}
          />
          <RatingButton right={5} top={5} rating={item.averageRating} />
          <CircleButton imgUrl={assets.heart} right={5} bottom={5} />
          <SellerButton left={5} top={5} />
        </PressableScale>
      </View>

      <View style={{ width: "100%", padding: spacing.sm }}>
        <ProductTitle
          title={item.name}
          companyName={item.companyName}
          titleSize={12}
        />
        <View
          style={{
            flexDirection: "row",
            alignItems: "center",
            marginTop: 5,
          }}
        >
          <Icon name="location" size={12} color={colors.textSecondary} />
          <Text
            variant="caption"
            weight="500"
            style={{ paddingLeft: 5 }}
          >
            {item.location}
          </Text>
        </View>
        <View style={{ flexDirection: "row", marginTop: 5 }}>
          <Text
            variant="caption"
            weight="500"
            color="accent"
            style={{ paddingLeft: 5 }}
          >
            {item.price}K
          </Text>
        </View>
        <View
          style={{
            flexDirection: "row",
            justifyContent: "space-between",
            alignItems: "center",
          }}
        >
          <Catalogue handlePress={() => {}} />
          {!checkItemExist(item.name) ? (
            <RectButton
              minWidth={50}
              fontSize={10}
              title="Add to cart"
              handlePress={async () => {
                if (item.name && item.price && item.images[0]) {
                  const newItem = { productId: item.id, quantity: 1 };
                  await addItem(newItem);
                  await getItems();
                  // This add bypasses cartService, so refresh the shared count badge.
                  cartService.notifyChange();
                }
              }}
            />
          ) : (
            <RectButton
              minWidth={50}
              fontSize={10}
              fontWeight="700"
              color="white"
              title="Checkout"
              handlePress={() => {
                router.push("/cart");
              }}
            />
          )}
        </View>
      </View>
    </Card>
  );
};

export default ProductCard;
