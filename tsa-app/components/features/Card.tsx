import React from "react";
import { Image, StyleSheet, Text, View, TouchableOpacity } from "react-native";
import { MaterialIcons } from "@expo/vector-icons";
import { COLORS, SIZES } from "../../constants";
import { router } from "expo-router";

interface CardProps {
  iconName?: string;
  title: string;
  buttonTitle?: string;
  imageUrl?: any;
}

const Card: React.FC<CardProps> = ({
  iconName,
  title,
  buttonTitle,
  imageUrl,
}) => {
  return (
    <TouchableOpacity
      onPress={() => {
        if (buttonTitle === "Register / Trade") {
          //@ts-ignore
          router.push("/products");
        } else {
          alert("Coming soon");
        }
      }}
      style={styles.content}
    >
      {iconName || imageUrl ? (
        <>
          {imageUrl ? (
            <Image source={imageUrl} resizeMode="contain" />
          ) : (
            <MaterialIcons
              //@ts-ignore
              name={iconName}
              size={30}
              color={COLORS.primary}
            />
          )}
          <Text style={styles.textStyle}>{title}</Text>
        </>
      ) : (
        <Text style={styles.textStyleBold}>{title}</Text>
      )}

      {buttonTitle && (
        <View style={styles.innerButton}>
          <Text style={styles.buttonText}>{buttonTitle}</Text>
        </View>
      )}
    </TouchableOpacity>
  );
};

export default Card;

const styles = StyleSheet.create({
  content: {
    width: SIZES.width * 0.4534883,
    alignContent: "center",
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: "#F5FCFF",
    padding: 10,
    marginTop: 5,
  },
  textStyle: {
    color: "black",
    textAlign: "center",
    marginTop: 8,
  },
  textStyleBold: {
    color: "black",
    textAlign: "center",
    marginTop: 8,
    fontSize: 14,
    fontWeight: "600",
  },
  innerButton: {
    borderRadius: 10,
    backgroundColor: COLORS.lightButton,
    padding: 5,
    margin: 5,
  },
  buttonText: {
    fontWeight: "500",
    fontSize: 12,
    padding: 3,
  },
});
