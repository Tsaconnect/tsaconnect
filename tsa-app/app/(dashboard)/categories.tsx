import React, { useState, useEffect } from "react";
import {
  View,
  Text,
  FlatList,
  Image,
  TouchableOpacity,
  StyleSheet,
  TextInput,
  Alert,
} from "react-native";
import { COLORS, SIZES } from "../../constants/theme";
import { useAuth } from "../../AuthContext/AuthContext";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { router } from "expo-router";
import Icon from "@expo/vector-icons/FontAwesome";

import LoadingSpinner from "../../components/others/LoadingSpinner";

const Categories = () => {
  const { token } = useAuth();
  const [categories, setCategories] = useState([]);
  const [loading, setLoading] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [filteredCategories, setFilteredCategories] = useState([]);

  const [user, setUser] = useState();

  async function getLoggedInUser() {
    try {
      const { data } = await axios.get(`${baseUrl}/users/me`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      setUser(data);
    } catch (error: any) {
      alert(error?.response.data.message);
    }
  }

  const fetchCategories = async () => {
    try {
      setLoading(true);
      const response = await axios.get(`${baseUrl}/category`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      setCategories(response.data.results);
      setFilteredCategories(response.data.results);
    } catch (error) {
      console.error("Error fetching categories:", error);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    if (token) {
      getLoggedInUser();
      fetchCategories();
    }
  }, [token]);

  useEffect(() => {
    setFilteredCategories(
      categories.filter((category) =>
        //@ts-ignore
        category.title.toLowerCase().includes(searchQuery.toLowerCase())
      )
    );
  }, [searchQuery, categories]);

  const handleDelete = async (id: any) => {
    try {
      setLoading(true);
      await axios.delete(`${baseUrl}/category/${id}`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      //@ts-ignore
      setCategories(categories.filter((category) => category.id !== id));
      Alert.alert("success", "The category was deleted successfully.");
    } catch (error) {
      console.error("Error deleting category:", error);
      Alert.alert("Error", "Error Deleting Category");
    } finally {
      setLoading(false);
    }
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <TextInput
          style={styles.searchInput}
          placeholder="Search Categories"
          value={searchQuery}
          onChangeText={(text) => setSearchQuery(text)}
        />
        <TouchableOpacity
          style={styles.addButton}
          onPress={() => router.push("/category/add")}
        >
          <Text style={styles.addButtonText}>Add Category</Text>
        </TouchableOpacity>
      </View>
      {loading ? (
        <LoadingSpinner />
      ) : (
        <>
          <FlatList
            data={
              showAll ? filteredCategories : filteredCategories.slice(0, 20)
            }
            //@ts-ignore
            keyExtractor={(item) => item.id.toString()}
            renderItem={({ item }) => (
              <View style={styles.categoryContainer}>
                <Image
                  //@ts-ignore
                  source={{ uri: item.image }}
                  style={styles.categoryImage}
                />
                <View style={styles.categoryInfo}>
                  <Text
                    //@ts-ignore
                    style={styles.categoryTitle}
                  >
                    {
                      //@ts-ignore
                      item.title
                    }
                  </Text>
                  {
                    //@ts-ignore
                    user?.role === "admin" && (
                      <View style={styles.actions}>
                        <TouchableOpacity
                          style={styles.actionButton}
                          onPress={() =>
                            router.push({
                              pathname: `/category/edit`,
                              params: item,
                            })
                          }
                        >
                          <Icon name="edit" size={20} color={COLORS.primary} />
                        </TouchableOpacity>
                        <TouchableOpacity
                          style={styles.actionButton}
                          //@ts-ignore
                          onPress={() => handleDelete(item.id)}
                        >
                          <Icon
                            name="trash"
                            size={20}
                            color={COLORS.secondary}
                          />
                        </TouchableOpacity>
                      </View>
                    )
                  }
                </View>
              </View>
            )}
            ListFooterComponent={
              !showAll && filteredCategories.length > 20 ? (
                <TouchableOpacity
                  style={styles.viewAllButton}
                  onPress={() => setShowAll(true)}
                >
                  <Text style={styles.viewAllButtonText}>View All</Text>
                </TouchableOpacity>
              ) : null
            }
          />
        </>
      )}
    </View>
  );
};

export default Categories;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: COLORS.white,
    padding: 20,
  },
  header: {
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
    marginBottom: 20,
  },
  addButton: {
    backgroundColor: COLORS.primary,
    padding: 10,
    borderRadius: 5,
    alignItems: "center",
  },
  addButtonText: {
    color: "#fff",
    fontSize: 16,
  },
  searchInput: {
    borderWidth: 1,
    borderColor: "#ccc",
    borderRadius: 5,
    padding: 10,
    width: "60%",
    backgroundColor: "#f9f9f9",
  },
  categoryContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 10,
    padding: 5,
    borderWidth: 1,
    borderColor: "#ccc",
    borderRadius: 5,
    backgroundColor: "#f9f9f9",
  },
  categoryImage: {
    width: 45,
    height: 45,
    borderRadius: 5,
    marginRight: 10,
  },
  categoryInfo: {
    flex: 1,
    flexDirection: "row",
    justifyContent: "space-between",
    alignItems: "center",
  },
  categoryTitle: {
    fontSize: 16,
    fontWeight: "bold",
  },
  actions: {
    flexDirection: "row",
  },
  actionButton: {
    marginLeft: 10,
  },
  viewAllButton: {
    padding: 10,
    alignItems: "center",
    borderWidth: 1,
    borderColor: COLORS.primary,
    borderRadius: 5,
  },
  viewAllButtonText: {
    color: COLORS.primary,
    fontSize: 16,
  },
});
