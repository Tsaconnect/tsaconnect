import React from "react";
import { View, StyleSheet } from "react-native";
import Icon from "@expo/vector-icons/FontAwesome";

type StarRatingProps = {
  rating: number;
  maxStars?: number;
};

const StarRating: React.FC<StarRatingProps> = ({ rating, maxStars = 5 }) => {
  const filledStars = Math.floor(rating);
  const halfStar = rating - filledStars >= 0.5;
  const emptyStars = maxStars - filledStars - (halfStar ? 1 : 0);

  return (
    <View style={styles.container}>
      {Array.from({ length: filledStars }).map((_, index) => (
        <Icon
          key={`star-filled-${index}`}
          name="star"
          size={20}
          color="#FFD700"
        />
      ))}
      {halfStar && <Icon name="star-half" size={20} color="#FFD700" />}
      {Array.from({ length: emptyStars }).map((_, index) => (
        <Icon
          key={`star-empty-${index}`}
          name="star-o"
          size={20}
          color="#FFD700"
        />
      ))}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flexDirection: "row",
  },
});

export default StarRating;
