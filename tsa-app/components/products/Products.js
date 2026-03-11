import { ImageBackground, StyleSheet, Text, View } from "react-native";
import { SIZES } from "../../constants";
import ProductList from "./productlist";

const Products = () => {
  return (
    <View style={styles.container}>
      <View style={styles.imageContainer}>
        <ImageBackground
          source={require("../../assets/images/productimage.png")}
          style={styles.backgroundImage}
          resizeMode="cover"
        >
          <Text style={styles.text}>Products</Text>
        </ImageBackground>
        
      </View>
        <ProductList/>
    </View>
  );
};

export default Products;

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  imageContainer: {
    flex: 1,
    flexDirection: "row",
    height:SIZES.height*(0.071888)
  },
  backgroundImage: {
    flex: 1,
    justifyContent: "center", // Aligns children components vertically
    alignItems: "center", // Aligns children components horizontally
  },
  text: {
    color: "white",
    fontSize: 24,
    fontWeight: "600",
  },
});
