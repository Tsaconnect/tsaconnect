import React from "react";
import { View, Text, Image, StyleSheet } from "react-native";
import { SIZES, COLORS } from "../../constants";

import notFound from "../../assets/icons/notfound.png";

const NotFound = ({ message }) => {
  return (
    <View style={styles.container}>
      <Image source={notFound} style={styles.image} resizeMode="contain" />
      <Text style={styles.text}>{message}</Text>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    height: SIZES.height * 0.7,
  },
  image: {
    width: 150,
    height: 150,
    marginBottom: 20,
  },
  text: {
    fontSize: 18,
    color: COLORS.gray,
  },
});

export default NotFound;
