import { Image, StyleSheet, Text, TouchableOpacity, View } from "react-native";
import React from "react";
import { COLORS, SIZES } from "../../constants";

interface ProductCardProps {
  title: string;
  image: string;
  handleItemClick: () => void;
}

const ProductCard: React.FC<ProductCardProps> = ({
  title,
  image,
  handleItemClick,
}) => {
  return (
    <TouchableOpacity onPress={handleItemClick} style={styles.card}>
      <Image
        source={{ uri: image }}
        style={{ width: "100%", height: "88%" }}
        resizeMode="contain"
      />
      <View style={{ alignItems: "center" }}>
        <Text>{title}</Text>
      </View>
    </TouchableOpacity>
  );
};

export default ProductCard;

const styles = StyleSheet.create({
  card: {
    width: SIZES.width * 0.444186,
    height: SIZES.height * 0.1952789699,
    marginHorizontal: 5,
    alignItems: "center",
    borderWidth: 1,
    borderRadius: 15,
    marginVertical: 10,
    justifyContent: "center",
  },
});
