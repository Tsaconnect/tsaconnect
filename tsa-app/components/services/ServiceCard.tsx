import { Image, StyleSheet, Text, View } from "react-native";
import React from "react";
import { COLORS, SIZES } from "../../constants";
import CardButton from "../buttons/CardButton";
import { router } from "expo-router";
//@ts-ignore
const ServiceCard = ({ id, title, image, description }) => {
  function selectService() {
    router.push({
      pathname: "/servicedetail",
      params: { id, title, image, description },
    });
  }
  return (
    <View style={styles.cardContainer}>
      <View style={styles.imageContainer}>
        <Image
          source={{ uri: image }}
          resizeMode="contain"
          style={{ width: "100%", height: "100%" }}
        />
      </View>
      <View style={styles.textContainer}>
        <View
          style={{
            backgroundColor: COLORS.white,
            width: SIZES.width * 0.9069767,
          }}
        >
          <Text style={styles.titleText}>{title}</Text>
          <Text>{description}</Text>
        </View>
        <View>
          <CardButton handlPress={selectService} title="Request Service" />
        </View>
      </View>
    </View>
  );
};

export default ServiceCard;

const styles = StyleSheet.create({
  cardContainer: {
    width: SIZES.width,
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: COLORS.white,
  },
  imageContainer: {
    width: "100%",
    height: SIZES.height * 0.370172,
  },
  textContainer: {
    width: "100%",
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: COLORS.white,
    alignContent: "center",
  },
  titleText: {
    fontWeight: "600",
    fontSize: 20,
  },
});
