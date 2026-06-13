// components/designs/Button.tsx
//
// Legacy product-card button set (CircleButton, RectButton, SellerButton,
// RatingButton) used by components/designs/productcard.tsx + designs/index.tsx.
// Migrated onto the TSA Signature design system (theme tokens via useTheme() +
// @/components/ui primitives). Every export, prop, the `...props`-spread-into-style
// behavior (callers pass positional ViewStyle props like right/top/bottom/left),
// the absolute positioning, and the rating/title rendering are preserved verbatim
// — this is a visual / structural restyle only.
import React from "react";
import {
  Image,
  ImageSourcePropType,
  ViewStyle,
  TextStyle,
} from "react-native";
import { useTheme } from "@/theme";
import { PressableScale, Text } from "@/components/ui";

// Type definitions for each component's props
type CircleButtonProps = {
  imgUrl: ImageSourcePropType;
  handlePress?: () => void;
} & ViewStyle;

type RectButtonProps = {
  minWidth?: number;
  fontSize?: number;
  title: string;
  handlePress?: () => void;
} & ViewStyle &
  TextStyle;

type SellerButtonProps = {
  handlePress?: () => void;
} & ViewStyle;

type RatingButtonProps = {
  rating?: number;
  handlePress?: () => void;
} & ViewStyle;

// CircleButton Component
export const CircleButton: React.FC<CircleButtonProps> = ({
  imgUrl,
  handlePress,
  ...props
}) => {
  const { colors, radius } = useTheme();
  return (
    <PressableScale
      style={{
        width: 30,
        height: 30,
        backgroundColor: colors.surface,
        position: "absolute",
        borderRadius: radius.pill,
        alignItems: "center",
        justifyContent: "center",
        shadowColor: colors.textSecondary,
        shadowOffset: { width: 0, height: 1 },
        shadowOpacity: 0.22,
        shadowRadius: 2.22,
        elevation: 3,
        ...props,
      }}
      onPress={handlePress}
    >
      <Image
        source={imgUrl}
        resizeMode="contain"
        style={{ width: 24, height: 24 }}
      />
    </PressableScale>
  );
};

// RectButton Component
export const RectButton: React.FC<RectButtonProps> = ({
  minWidth = 50,
  fontSize = 14,
  handlePress,
  title,
  ...props
}) => {
  const { colors, radius, spacing } = useTheme();
  return (
    <PressableScale
      style={{
        backgroundColor: colors.accentHover,
        borderRadius: radius.md,
        minWidth: minWidth,
        padding: spacing.sm,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text
        style={{
          fontSize: fontSize,
          lineHeight: Math.round(fontSize * 1.3),
          textAlign: "center",
          color: colors.textPrimary,
        }}
      >
        {title}
      </Text>
    </PressableScale>
  );
};

// SellerButton Component
export const SellerButton: React.FC<SellerButtonProps> = ({
  handlePress,
  ...props
}) => {
  const { colors, radius } = useTheme();
  return (
    <PressableScale
      style={{
        backgroundColor: colors.surface,
        position: "absolute",
        borderRadius: radius.sm,
        alignItems: "center",
        justifyContent: "center",
        shadowColor: colors.textSecondary,
        shadowOffset: { width: 0, height: 1 },
        shadowOpacity: 0.22,
        shadowRadius: 2.22,
        elevation: 3,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text weight="500" style={{ padding: 5, fontSize: 11, lineHeight: 14 }}>
        Best seller
      </Text>
    </PressableScale>
  );
};

// RatingButton Component
export const RatingButton: React.FC<RatingButtonProps> = ({
  rating = 4.7,
  handlePress,
  ...props
}) => {
  const { colors, radius } = useTheme();
  return (
    <PressableScale
      style={{
        backgroundColor: colors.surface,
        position: "absolute",
        borderRadius: radius.sm,
        alignItems: "center",
        justifyContent: "center",
        shadowColor: colors.textSecondary,
        shadowOffset: { width: 0, height: 1 },
        shadowOpacity: 0.22,
        shadowRadius: 2.22,
        elevation: 3,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text
        weight="500"
        style={{
          padding: 5,
          fontSize: 14,
          lineHeight: 18,
          color: colors.accent,
        }}
      >
        *{rating.toFixed(1)}
      </Text>
    </PressableScale>
  );
};
