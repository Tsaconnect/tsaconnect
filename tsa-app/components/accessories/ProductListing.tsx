import {
  Button,
  Image,
  StyleSheet,
  Text,
  View,
} from "react-native";
import React from "react";
import { router } from "expo-router";

interface ProductListingProps {
  title: string;
  image: string;
  description: string;
  location: string;
  rating: string;
  priceRange: string;
}

const ProductListing: React.FC<ProductListingProps> = ({
  title,
  image,
  description,
  location,
  rating,
  priceRange,
}) => {
  function selectService() {
    router.push({
      //@ts-ignore
      pathname: "/productdetails",
      params: { title, image, description },
    });
  }

  return (
    <View style={styles.card}>
      <Text style={styles.bestSeller}>Best seller</Text>
      <Image source={{ uri: image }} style={styles.image} />
      <Text style={styles.rating}>{rating}</Text>
      <Text style={styles.name}>{title}</Text>
      <Text style={styles.location}>{location}</Text>
      <Text style={styles.priceRange}>{priceRange}</Text>
      <Button title="Buy Now" onPress={selectService} />
    </View>
  );
};

export default ProductListing;

const styles = StyleSheet.create({
  card: {
    backgroundColor: "#fff",
    borderRadius: 10,
    padding: 10,
    marginBottom: 20,
    shadowColor: "#000",
    shadowOpacity: 0.2,
    shadowRadius: 10,
    shadowOffset: { width: 0, height: 5 },
  },
  bestSeller: {
    backgroundColor: "#FFC107",
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: 5,
    alignSelf: "flex-start",
    marginBottom: 10,
    color: "#fff",
  },
  image: {
    width: "100%",
    height: 150,
    borderRadius: 10,
    marginBottom: 10,
  },
  rating: {
    position: "absolute",
    top: 10,
    right: 10,
    backgroundColor: "#fff",
    paddingHorizontal: 8,
    paddingVertical: 2,
    borderRadius: 5,
    color: "#000",
  },
  name: {
    fontSize: 16,
    fontWeight: "bold",
    marginBottom: 5,
  },
  location: {
    fontSize: 14,
    color: "#666",
    marginBottom: 5,
  },
  priceRange: {
    fontSize: 14,
    color: "#666",
    marginBottom: 10,
  },
});
