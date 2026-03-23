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
import { COLORS } from "../../constants/theme";
import { router } from "expo-router";
import Icon from "@expo/vector-icons/FontAwesome";
import api from "@/components/services/api";

import LoadingSpinner from "../../components/others/LoadingSpinner";

const Categories = () => {
  const [categories, setCategories] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [filteredCategories, setFilteredCategories] = useState<any[]>([]);
  const [userRole, setUserRole] = useState<string | null>(null);

  const initialize = async () => {
    try {
      // Ensure api has token loaded
      const token = await api.getStoredToken();
      if (token) {
        api.setToken(token);
      }

      // Fetch user profile for role check
      const profileRes = await api.getProfile();
      if (profileRes.success && profileRes.data) {
        setUserRole(profileRes.data.role);
      }

      // Fetch categories
      const response = await api.getCategories({ active: true });
      if (response.success && response.data) {
        const list = Array.isArray(response.data) ? response.data : [];
        setCategories(list);
        setFilteredCategories(list);
      } else {
        setCategories([]);
        setFilteredCategories([]);
      }
    } catch (error: any) {
      console.error("Error loading categories:", error);
      setCategories([]);
      setFilteredCategories([]);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    initialize();
  }, []);

  useEffect(() => {
    setFilteredCategories(
      categories.filter((category) =>
        category.title?.toLowerCase().includes(searchQuery.toLowerCase())
      )
    );
  }, [searchQuery, categories]);

  const handleDelete = async (id: any) => {
    try {
      setLoading(true);
      const response = await api.deleteCategory(id);
      if (response.success) {
        setCategories(categories.filter((category) => category.id !== id));
        Alert.alert("Success", "The category was deleted successfully.");
      } else {
        Alert.alert("Error", response.message || "Error deleting category");
      }
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
          onPress={() => router.push("/add-category")}
        >
          <Text style={styles.addButtonText}>Add Category</Text>
        </TouchableOpacity>
      </View>
      {loading ? (
        <LoadingSpinner />
      ) : (
        <FlatList
          data={showAll ? filteredCategories : filteredCategories.slice(0, 20)}
          keyExtractor={(item) => item.id?.toString() || item._id}
          renderItem={({ item }) => (
            <View style={styles.categoryContainer}>
              <Image
                source={{ uri: item.image }}
                style={styles.categoryImage}
              />
              <View style={styles.categoryInfo}>
                <Text style={styles.categoryTitle}>{item.title}</Text>
              </View>
            </View>
          )}
          ListEmptyComponent={
            <View style={styles.emptyContainer}>
              <Icon name="folder-open" size={48} color="#D4B896" />
              <Text style={styles.emptyTitle}>No categories found</Text>
            </View>
          }
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
    flex: 1,
    marginRight: 10,
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
    flex: 1,
  },
  actions: {
    flexDirection: "row",
  },
  actionButton: {
    marginLeft: 10,
  },
  emptyContainer: {
    alignItems: "center",
    justifyContent: "center",
    paddingVertical: 80,
  },
  emptyTitle: {
    fontSize: 18,
    fontWeight: "600",
    color: "#999",
    marginTop: 16,
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
