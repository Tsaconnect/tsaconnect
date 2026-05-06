import { Image, StyleSheet, Text, View } from "react-native";
import React from "react";
import Icon from "react-native-vector-icons/MaterialIcons";
import { COLORS } from "../../constants";
import CardButton from "../buttons/CardButton";
import { router } from "expo-router";

interface ServiceCardProps {
  id: string;
  title: string;
  image?: string;
  description?: string;
}

const ServiceCard: React.FC<ServiceCardProps> = ({ id, title, image, description }) => {
  const hasImage = typeof image === "string" && image.length > 0;

  const selectService = () => {
    router.push({
      pathname: "/servicedetail",
      params: { id, title, image: image ?? "", description: description ?? "" },
    });
  };

  return (
    <View style={styles.card}>
      <View style={styles.imageContainer}>
        {hasImage ? (
          <Image source={{ uri: image }} resizeMode="cover" style={styles.image} />
        ) : (
          <View style={styles.imagePlaceholder}>
            <Icon name="storefront" size={48} color={COLORS.primary} />
          </View>
        )}
      </View>
      <View style={styles.body}>
        <Text style={styles.title} numberOfLines={2}>
          {title}
        </Text>
        {description ? (
          <Text style={styles.description} numberOfLines={3}>
            {description}
          </Text>
        ) : null}
        <View style={styles.buttonWrap}>
          <CardButton handlPress={selectService} title="Request Service" />
        </View>
      </View>
    </View>
  );
};

export default ServiceCard;

const styles = StyleSheet.create({
  card: {
    backgroundColor: COLORS.white,
    borderRadius: 12,
    marginHorizontal: 16,
    marginVertical: 8,
    overflow: "hidden",
    shadowColor: "#000",
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.06,
    shadowRadius: 4,
    elevation: 2,
  },
  imageContainer: {
    width: "100%",
    height: 180,
    backgroundColor: "#FAFAFA",
  },
  image: {
    width: "100%",
    height: "100%",
  },
  imagePlaceholder: {
    flex: 1,
    alignItems: "center",
    justifyContent: "center",
    backgroundColor: "#FFF5EA",
  },
  body: {
    padding: 16,
  },
  title: {
    fontWeight: "700",
    fontSize: 18,
    color: "#1A1A1A",
    marginBottom: 6,
  },
  description: {
    fontSize: 14,
    color: "#666",
    lineHeight: 20,
    marginBottom: 14,
  },
  buttonWrap: {
    alignItems: "center",
  },
});
