// components/accessories/ProductListCard.tsx
//
// Product / service category list card. Restyled onto the TSA Signature design
// system: legacy constants/theme (COLORS/SIZES) and the hand-rolled StyleSheet
// are replaced by useTheme tokens + @/components/ui primitives (Card, Avatar,
// Text, Icon, EmptyState, Button, PressableScale). The props interface, exports,
// AppContext.setCategory wiring, the isDisabled debounce, the "digital → coming
// soon" alert, the empty-state "Register" link, and the navigation route/params
// are all unchanged — this is a visual / structural migration only.
import React, { useContext, useState } from "react";
import { ScrollView, View, Image } from "react-native";
import { Link, router } from "expo-router";
import { AppContext } from "../../AuthContext/AuthContext";
import { useTheme } from "@/theme";
import { Avatar, Card, EmptyState, Icon, PressableScale, Text } from "@/components/ui";

interface Item {
  id: string;
  title: string;
  // Backend categories carry `icon` (URL or asset name); some legacy data
  // shipped `image`. Accept either so we render whatever the API provides.
  icon?: string;
  image?: string;
}

interface ProductListCardProps {
  itemList: Item[];
  itemValue: string;
}

const ProductListCard: React.FC<ProductListCardProps> = ({
  itemList,
  itemValue,
}) => {
  const { setCategory } = useContext(AppContext);
  const [isDisabled, setIsDisabled] = useState(false);
  const { colors, spacing, radius } = useTheme();

  return (
    <ScrollView
      style={{ flex: 1, backgroundColor: colors.background }}
      contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm }}
    >
      {itemList.length === 0 ? (
        <EmptyState
          icon="storefront"
          title="No registered available vendor merchant at the moment."
          subtitle="Do you offer such product or service?"
        />
      ) : (
        itemList.map((item, itemIndex) => (
          <PressableScale
            onPress={() => {
              if (itemValue === "digital") {
                alert("coming soon");
                return;
              }
              if (!isDisabled) {
                setIsDisabled(true);
                setCategory(item.title);
                router.push({
                  //@ts-ignore
                  pathname: `/${itemValue}`,
                  params: { value: item.id },
                });
                setTimeout(() => {
                  setIsDisabled(false);
                }, 2000);
              }
            }}
            key={itemIndex}
            disabled={isDisabled}
          >
            <Card
              padding="md"
              style={{
                flexDirection: "row",
                alignItems: "center",
                gap: spacing.md,
              }}
            >
              {item.icon || item.image ? (
                <Image
                  source={{ uri: item.icon || item.image }}
                  style={{
                    width: 44,
                    height: 44,
                    borderRadius: radius.pill,
                    backgroundColor: colors.surfaceAlt,
                  }}
                  resizeMode="contain"
                />
              ) : (
                <Avatar name={item.title} size={44} />
              )}
              <Text variant="bodyMed" weight="600" style={{ flex: 1 }} numberOfLines={2}>
                {item.title}
              </Text>
              <Icon name="chevronRight" size={20} color={colors.textMuted} />
            </Card>
          </PressableScale>
        ))
      )}

      {itemList.length === 0 && (
        <View style={{ alignItems: "center", marginTop: spacing.md }}>
          <Link
            //@ts-ignore
            href="/serviceaction"
            style={{
              fontSize: 16,
              color: colors.accent,
              paddingVertical: spacing.sm,
            }}
          >
            Register
          </Link>
        </View>
      )}
    </ScrollView>
  );
};

export default ProductListCard;
