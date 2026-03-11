import React from "react";
import {
  TouchableOpacity,
  View,
  Text,
  Image,
  ImageSourcePropType,
  ViewStyle,
  TextStyle,
} from "react-native";
import { COLORS, SHADOWS, SIZES } from "../../constants";

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
  return (
    <TouchableOpacity
      style={{
        width: 30,
        height: 30,
        backgroundColor: COLORS.white,
        position: "absolute",
        borderRadius: SIZES.extraLarge,
        alignItems: "center",
        justifyContent: "center",
        ...SHADOWS.light,
        ...props,
      }}
      onPress={handlePress}
    >
      <Image
        source={imgUrl}
        resizeMode="contain"
        style={{ width: 24, height: 24 }}
      />
    </TouchableOpacity>
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
  return (
    <TouchableOpacity
      style={{
        backgroundColor: COLORS.lightButton,
        borderRadius: SIZES.medium,
        minWidth: minWidth,
        padding: SIZES.base,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text
        style={{
          fontSize: fontSize,
          textAlign: "center",
          color: COLORS.black,
        }}
      >
        {title}
      </Text>
    </TouchableOpacity>
  );
};

// SellerButton Component
export const SellerButton: React.FC<SellerButtonProps> = ({
  handlePress,
  ...props
}) => {
  return (
    <TouchableOpacity
      style={{
        backgroundColor: COLORS.white,
        position: "absolute",
        borderRadius: SIZES.base,
        alignItems: "center",
        justifyContent: "center",
        ...SHADOWS.light,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text style={{ padding: 5, fontSize: 11, fontWeight: "500" }}>
        Best seller
      </Text>
    </TouchableOpacity>
  );
};

// RatingButton Component
export const RatingButton: React.FC<RatingButtonProps> = ({
  rating = 4.7,
  handlePress,
  ...props
}) => {
  return (
    <TouchableOpacity
      style={{
        backgroundColor: COLORS.white,
        position: "absolute",
        borderRadius: SIZES.base,
        alignItems: "center",
        justifyContent: "center",
        ...SHADOWS.light,
        ...props,
      }}
      onPress={handlePress}
    >
      <Text
        style={{
          padding: 5,
          fontSize: 14,
          fontWeight: "500",
          color: COLORS.primary,
        }}
      >
        *{rating.toFixed(1)}
      </Text>
    </TouchableOpacity>
  );
};
