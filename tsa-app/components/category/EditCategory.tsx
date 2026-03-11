import React, { useState, useEffect } from "react";
import {
  View,
  Text,
  ScrollView,
  StyleSheet,
  TextInput,
  TouchableOpacity,
  Image,
  Alert,
} from "react-native";
import { COLORS, SIZES } from "../../constants/theme";
import Icon from "@expo/vector-icons/FontAwesome";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import { router, useLocalSearchParams } from "expo-router";
import axios from "axios";
import { Picker } from "@react-native-picker/picker";
import { generateFileName } from "../../constants/api/filename";
import { baseUrl } from "../../constants/api/apiClient";
import LoadingSpinner from "../others/LoadingSpinner";

const EditCategoryScreen = () => {
  const { token } = useAuth();

  const item = useLocalSearchParams();

  const [description, setDescription] = useState(item.description || "");
  const [title, setTitle] = useState(item.title || "");
  const [type, setType] = useState(item.type || "");
  const [featuredImage, setFeaturedImage] = useState(
    item.featuredImage || null
  );
  const [parentCategory, setParentCategory] = useState(
    item.parentCategory || ""
  );
  const [isFeatured, setIsFeatured] = useState(item.isFeatured || false);
  const [isParent, setIsParent] = useState(item.isParent || false);
  const [hasParent, setHasParent] = useState(item.hasParent || false);
  const [parentCategories, setParentCategories] = useState([]);
  const [loading, setLoading] = useState(false);

  const fetchParentCategories = async () => {
    try {
      const response = await axios.get(`${baseUrl}/category?isParent=true`, {
        headers: {
          Authorization: `${token}`,
        },
      });
      setParentCategories(response.data.results);
    } catch (error) {
      console.error("Error fetching parent categories:", error);
    }
  };

  useEffect(() => {
    fetchParentCategories();
  }, []);

  const pickImageAsync = async () => {
    try {
      const { status } =
        await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== "granted") {
        Alert.alert("Permission to access media library denied");
        return;
      }

      let result = await ImagePicker.launchImageLibraryAsync({
        allowsEditing: false,
        aspect: [4, 3],
        quality: 1,
      });

      if (!result.canceled) {
        setFeaturedImage(result.assets[0].uri);
      } else {
        Alert.alert("You did not select any image.");
      }
    } catch (error) {
      console.error("Error picking image:", error);
    }
  };

  const updateCategory = async () => {
    if (!title || !type) {
      //@ts-ignore
      Alert.alert({
        type: "error",
        text1: "Title and type are required",
      });
      return;
    }
    const formData = new FormData();
    if (featuredImage && featuredImage !== item.featuredImage) {
      //@ts-ignore
      formData.append("image", {
        uri: featuredImage,
        type: "image/jpeg",
        name: `image_${generateFileName()}.jpg`,
      });
    }
    //@ts-ignore
    formData.append("title", title);
    //@ts-ignore
    formData.append("description", description);
    //@ts-ignore
    formData.append("type", type);
    //@ts-ignore
    formData.append("isFeatured", isFeatured);
    //@ts-ignore
    formData.append("isParent", isParent);
    if (parentCategory) {
      //@ts-ignore
      formData.append("parentCategory", parentCategory);
    }

    try {
      setLoading(true);
      const response = await axios.patch(
        `${baseUrl}/category/${item.id}`,
        formData,
        {
          headers: {
            Authorization: token,
            "Content-Type": "multipart/form-data",
          },
        }
      );
      if (response) {
        Alert.alert("success", "The category was updated successfully.");
        //@ts-ignore
        router.push("/category");
      }
    } catch (error) {
      console.error("Error Updating Category", error);
      Alert.alert("error", "Error Updating Category");
    } finally {
      setLoading(false);
    }
  };

  return (
    <View style={{ backgroundColor: "#fff" }}>
      <ScrollView style={styles.cover}>
        <Text style={styles.label}>Title</Text>
        <TextInput
          style={styles.input}
          //@ts-ignore
          value={title}
          onChangeText={(e) => setTitle(e)}
        />
        <Text style={styles.label}>Enter Description</Text>
        <TextInput
          style={styles.input}
          //@ts-ignore
          value={description}
          onChangeText={(e) => setDescription(e)}
          autoCapitalize="none"
        />
        <Text style={styles.label}>Enter Category Type</Text>
        <Picker
          style={styles.input}
          selectedValue={type}
          onValueChange={(itemValue) => setType(itemValue)}
        >
          <Picker.Item label={"Select Type"} value={""} />
          <Picker.Item label={"Product"} value={"Product"} />
          <Picker.Item label={"Service"} value={"Service"} />
        </Picker>

        <Text style={styles.label}>Upload Featured Image</Text>
        <View style={styles.uploadContainer}>
          <TouchableOpacity onPress={pickImageAsync}>
            <Icon name="upload" size={25} color="#aaa" />
          </TouchableOpacity>
        </View>

        {featuredImage && (
          <View style={styles.imageContainer}>
            <Image
              //@ts-ignore
              source={{ uri: featuredImage }}
              style={styles.uploadedImage}
            />
          </View>
        )}

        <TouchableOpacity
          style={styles.checkboxContainer}
          onPress={() => setHasParent(!hasParent)}
        >
          <Icon
            name={hasParent ? "check-square-o" : "square-o"}
            size={25}
            color="#000"
          />
          <Text style={styles.checkboxLabel}>Select Parent Category</Text>
        </TouchableOpacity>
        {hasParent && (
          <Picker
            style={styles.input}
            selectedValue={parentCategory}
            onValueChange={(itemValue) => setParentCategory(itemValue)}
          >
            <Picker.Item label={"Select Parent Category"} value={""} />
            {parentCategories.map((item, index) => (
              //@ts-ignore
              <Picker.Item key={index} label={item.title} value={item.id} />
            ))}
          </Picker>
        )}

        <Text style={styles.label}>Parent Category?</Text>
        <TouchableOpacity
          style={styles.checkboxContainer}
          onPress={() => setIsParent(!isParent)}
        >
          <Icon
            name={isParent ? "check-square-o" : "square-o"}
            size={25}
            color="#000"
          />
          <Text style={styles.checkboxLabel}>Is Parent</Text>
        </TouchableOpacity>

        <Text style={styles.label}>Featured?</Text>
        <TouchableOpacity
          style={styles.checkboxContainer}
          onPress={() => setIsFeatured(!isFeatured)}
        >
          <Icon
            name={isFeatured ? "check-square-o" : "square-o"}
            size={25}
            color="#000"
          />
          <Text style={styles.checkboxLabel}>Is Featured</Text>
        </TouchableOpacity>

        <View style={{ alignItems: "center", marginBottom: 55 }}>
          <TouchableOpacity
            //@ts-ignore
            style={[styles.button, loading && styles.buttonDisabled]}
            onPress={updateCategory}
            disabled={loading}
          >
            {loading ? (
              <LoadingSpinner />
            ) : (
              <Text style={styles.buttonText}>Update Category</Text>
            )}
          </TouchableOpacity>
        </View>
      </ScrollView>
    </View>
  );
};

export default EditCategoryScreen;

const styles = StyleSheet.create({
  container: {
    justifyContent: "center",
    padding: 20,
    backgroundColor: COLORS.white,
  },
  input: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
    marginTop: 2,
    marginBottom: 10,
    padding: 10,
    borderRadius: 5,
    marginHorizontal: SIZES.width * 0.05,
    backgroundColor: COLORS.gray,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: "center",
    justifyContent: "center",
    height: 0.0687 * SIZES.height,
    borderRadius: 10,
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginBottom: 20,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  label: {
    marginHorizontal: SIZES.width * 0.05,
  },
  uploadContainer: {
    alignItems: "center",
    justifyContent: "center",
    width: SIZES.width * 0.9,
    height: 100,
    borderColor: "#ccc",
    borderWidth: 1,
    borderRadius: 5,
    marginBottom: 20,
    backgroundColor: "#f9f9f9",
    marginHorizontal: SIZES.width * 0.05,
  },
  uploadedImage: {
    width: 100,
    height: 100,
    resizeMode: "contain",
    marginHorizontal: SIZES.width * 0.05,
  },
  imageContainer: {
    alignItems: "center",
    marginBottom: 20,
  },
  cover: {
    backgroundColor: COLORS.white,
  },
  checkboxContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginHorizontal: SIZES.width * 0.05,
    marginBottom: 10,
  },
  checkboxLabel: {
    marginLeft: 10,
  },
});
