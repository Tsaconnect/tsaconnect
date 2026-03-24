import React, { useEffect, useState } from "react";
import {
  View,
  Text,
  Image,
  StyleSheet,
  TouchableOpacity,
  ScrollView,
} from "react-native";
import Icon from "@expo/vector-icons/FontAwesome";
import { COLORS } from "../../constants";
import { RectButton } from "../designs";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { useAuth } from "../../AuthContext/AuthContext";
import { router } from "expo-router";
//@ts-ignore
const ProductDetail = ({ item }) => {
  const [isMore, setIsMore] = useState(false);
  const { items, token, setItems } = useAuth();
  console.log(item.attributes);
  //@ts-ignore
  const addItem = async (newItem) => {
    try {
      const response = await axios.post(`${baseUrl}/cart`, newItem, {
        headers: {
          Authorization: `${token}`,
          "Content-Type": "application/json",
        },
      });
      if (response.status === 201) {
        return response.data;
      }
    } catch (error) {
      throw error;
    }
  };
  //@ts-ignore
  const checkItemExist = (name) => {
    return items.some((item) => item.name === name);
  };

  const getItems = async () => {
    try {
      const response = await axios.get(`${baseUrl}/cart`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      if (response.status === 200) {
        setItems(response.data);
      }
    } catch (error) {
      console.error(error);
    }
  };

  useEffect(() => {
    getItems();
  }, []);

  const renderStars = (rating: number) => {
    const stars = [];
    for (let i = 1; i <= 5; i++) {
      stars.push(
        <Icon
          key={i}
          name="star"
          size={20}
          color={i <= rating ? "gold" : "lightgray"}
          style={styles.star}
        />
      );
    }
    return stars;
  };

  const images: string[] = Array.isArray(item?.images)
    ? item.images
        .filter((img: any) => img.url && img.url.startsWith("http"))
        .sort((a: any, b: any) => (a.order ?? 0) - (b.order ?? 0))
        .map((img: any) => img.url)
    : [];

  return (
    <ScrollView style={styles.container}>
      {item && (
        <>
          <ScrollView horizontal pagingEnabled showsHorizontalScrollIndicator={false} style={styles.pagerView}>
            {images.map((image: string, index: number) => (
              <View style={styles.page} key={index}>
                <Image
                  source={{ uri: image }}
                  style={styles.image}
                  accessibilityLabel="Product Image"
                />
              </View>
            ))}
          </ScrollView>

          <View style={styles.contentContainer}>
            <View style={styles.titleContainer}>
              <Text style={styles.title}>
                {item.companyName}
                {"'s "}
                {item.name}
              </Text>
              <View style={styles.locationContainer}>
                <View style={styles.locationRow}>
                  <Icon
                    name="map-marker"
                    size={20}
                    color="gray"
                    style={styles.locationIcon}
                  />
                  <Text style={styles.location}>{item.location}</Text>
                </View>
                <View style={styles.ratingRow}>
                  {renderStars(item.averageRating)}
                  <Text style={styles.rating}>{item.averageRating}</Text>
                  <Text style={styles.ratingCount}>
                    {item.reviews?.length || 0} ratings
                  </Text>
                </View>
              </View>
              <View style={styles.priceContainer}>
                <Text style={styles.price}>{item.price}</Text>
              </View>
              <View style={styles.feeContainer}>
                <Text style={styles.shipping}>15 items sold</Text>
              </View>
            </View>

            <View style={styles.descriptionContainer}>
              <Text style={styles.descriptionTitle}>Description</Text>
              <Text style={styles.descriptionText}>
                {isMore ? item.description : item.description.slice(0, 100)}
              </Text>
              {!isMore && (
                <TouchableOpacity
                  onPress={() => setIsMore(true)}
                  accessibilityRole="button"
                  accessibilityLabel="See more details"
                >
                  <Text style={styles.viewMoreText}>See more</Text>
                </TouchableOpacity>
              )}
            </View>
            {isMore && (
              <>
                <View style={styles.productInfoContainer}>
                  {(Array.isArray(item.attributes) ? item.attributes : [])
                    .map((attribute: any, index: number) => (
                      <View style={styles.productInfoRow} key={index}>
                        <Text style={styles.productInfoTitle}>
                          {attribute.name}:{" "}
                        </Text>
                        <Text style={styles.productInfoValue}>
                          {attribute.value}
                        </Text>
                      </View>
                    ))}
                </View>
                <TouchableOpacity
                  onPress={() => setIsMore(false)}
                  style={styles.viewLessButton}
                  accessibilityRole="button"
                  accessibilityLabel="View less details"
                >
                  <Text style={styles.viewMoreText}>View less</Text>
                </TouchableOpacity>
              </>
            )}
            <View style={styles.reviewsContainer}>
              <Text style={styles.reviewRating}>
                Verified Customer Feedback
              </Text>
              <Text style={styles.reviewTitle}>Product rating and review</Text>
              <View style={{ flexDirection: "row" }}>
                <Text style={styles.reviewScore}>{item.averageRating}/5</Text>
                <Text style={styles.reviewCount}>
                  {item.reviews?.length || 0} ratings so far
                </Text>
              </View>

              {(Array.isArray(item.reviews) ? item.reviews : []).map((review: any, index: number) => (
                <View key={index} style={styles.review}>
                  <View style={styles.reviewHeader}>
                    <View style={{ flexDirection: "row" }}>
                      {renderStars(review.rating)}
                    </View>
                    <Text style={styles.reviewDate}>{review.date}</Text>
                  </View>
                  <Text style={styles.reviewAuthor}>{review.reviewedBy}</Text>
                  <Text style={styles.reviewText}>{review.reviewText}</Text>
                </View>
              ))}
            </View>
            <View>
              {!checkItemExist(item.name) ? (
                <RectButton
                  minWidth={50}
                  fontSize={10}
                  title="Add to cart"
                  handlePress={async () => {
                    if (item.name && item.price && images[0]) {
                      const newItem = {
                        productId: item.id, // unique id
                        quantity: 1,
                      };
                      const response = await addItem(newItem);
                      await getItems();
                    }
                  }}
                />
              ) : (
                <RectButton
                  minWidth={50}
                  fontSize={10}
                  fontWeight="700"
                  color="white"
                  title="Checkout"
                  handlePress={() => {
                    router.push("/cart");
                  }}
                />
              )}
            </View>
          </View>
        </>
      )}
    </ScrollView>
  );
};

export default ProductDetail;

const styles = StyleSheet.create({
  container: {
    backgroundColor: COLORS.white,
  },
  pagerView: {
    height: 300,
  },
  page: {
    justifyContent: "center",
    alignItems: "center",
  },
  image: {
    width: "100%",
    height: 300,
    resizeMode: "contain",
  },
  contentContainer: {
    padding: 20,
  },
  titleContainer: {
    marginBottom: 20,
    borderBottomWidth: 1,
    borderBottomColor: "#d3d3d3",
    paddingBottom: 10,
  },
  title: {
    fontSize: 18,
    fontWeight: "bold",
    lineHeight: 22,
    marginBottom: 10,
  },
  locationContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 10,
    flexWrap: "wrap",
  },
  locationRow: {
    flexDirection: "row",
    alignItems: "center",
    flex: 1,
  },
  ratingRow: {
    flexDirection: "row",
    alignItems: "center",
    flex: 1,
    justifyContent: "flex-end",
  },
  locationIcon: {
    marginRight: 5,
  },
  location: {
    marginRight: 10,
  },
  star: {
    marginRight: 2,
  },
  rating: {
    marginLeft: 5,
    marginRight: 10,
  },
  ratingCount: {
    color: "gray",
  },
  priceContainer: {
    marginBottom: 10,
    flexDirection: "row",
    flexWrap: "wrap",
    justifyContent: "space-between",
    alignItems: "center",
  },
  price: {
    fontSize: 20,
    fontWeight: "bold",
    marginBottom: 5,
    lineHeight: 22,
    color: "#9D6B38",
  },
  feeContainer: {
    marginBottom: 10,
    flexDirection: "row",
    flexWrap: "wrap",
    justifyContent: "space-between",
    alignItems: "center",
  },
  shipping: {
    fontSize: 11,
    lineHeight: 22,
    color: "#000",
  },
  productInfoContainer: {
    marginBottom: 20,
  },
  productInfoRow: {
    flexDirection: "row",
    marginBottom: 10,
    justifyContent: "space-between",
  },
  productInfoTitle: {
    fontSize: 14,
    fontWeight: "bold",
    marginBottom: 5,
    color: "#9C9C9C",
    letterSpacing: -0.41,
  },
  productInfoValue: {
    fontSize: 14,
    letterSpacing: -0.41,
  },
  viewMoreButton: {
    alignItems: "center",
    marginVertical: 10,
  },
  viewLessButton: {
    alignItems: "center",
    marginBottom: 50,
  },
  viewMoreText: {
    color: "#E8A14A",
    fontSize: 12,
  },
  descriptionTitle: {
    fontSize: 18,
    fontWeight: "bold",
    marginBottom: 10,
  },
  descriptionText: {
    fontSize: 14,
    marginBottom: 15,
    lineHeight: 22,
  },
  descriptionContainer: {
    marginBottom: 20,
  },
  reviewsContainer: {
    backgroundColor: COLORS.gray,
  },
  reviewTitle: {
    fontSize: 16,
    fontWeight: "bold",
    marginBottom: 5,
  },
  reviewRating: {
    fontSize: 12,
    marginBottom: 5,
  },
  reviewScore: {
    fontSize: 12,
    color: "#E8A14A",
    fontWeight: "bold",
    marginBottom: 5,
  },
  reviewCount: {
    fontSize: 12,
    marginBottom: 20,
  },
  review: {
    marginBottom: 20,
    borderBottomWidth: 1,
    borderBottomColor: "#d3d3d3",
    paddingBottom: 10,
  },
  reviewHeader: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    marginBottom: 5,
  },
  reviewAuthor: {
    fontSize: 14,
    fontWeight: "bold",
    marginBottom: 5,
  },
  reviewDate: {
    fontSize: 12,
    color: "gray",
  },
  reviewText: {
    fontSize: 12,
  },
});
