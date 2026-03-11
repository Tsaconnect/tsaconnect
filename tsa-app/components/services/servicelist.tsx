import React, { useState, useEffect } from "react";
import {
  ScrollView,
  View,
  Text,
  Image,
  StyleSheet,
  Pressable,
} from "react-native";
import { COLORS } from "../../constants";
import { useAuth } from "../../AuthContext/AuthContext";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { router } from "expo-router";

const ServiceList = () => {
  const [categories, setCategories] = useState([]);
  const { token } = useAuth();

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(`${baseUrl}/category?type=Service`, {
          headers: {
            Authorization: `${token}`,
          },
        });
        const fetchedCategories = response.data.results.slice(0, 8);
        setCategories(fetchedCategories);
      } catch (error) {}
    };
    fetchCategories();
  }, []);

  return (
    <ScrollView style={styles.container}>
      {categories.map((item, itemIndex) => (
        <Pressable
          key={itemIndex}
          style={styles.itemContainer}
          onPress={() =>
            router.push({
              pathname: "/categoryservice",
              //@ts-ignore
              params: { value: item.id },
            })
          }
        >
          <Image
            //@ts-ignore
            source={{ uri: item.image }}
            style={styles.iconStyle}
            resizeMode="contain"
          />
          <Text style={styles.itemText}>
            {
              //@ts-ignore
              item.title
            }
          </Text>
        </Pressable>
      ))}
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 10,
    backgroundColor: "#fff",
  },
  itemContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 5,
    borderColor: COLORS.gray,
    borderBottomWidth: 0.5,
    paddingVertical: 10,
  },
  iconStyle: {
    width: 50,
    height: 50,
    marginRight: 10,
  },
  itemText: {
    fontSize: 16,
  },
});

export default ServiceList;
