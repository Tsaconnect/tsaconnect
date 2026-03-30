import {
  Image,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
  Modal,
  ToastAndroid,
} from "react-native";
import React, { useState, useEffect, useCallback } from "react";
import { COLORS, SIZES } from "../../constants/theme";
import Icon from "@expo/vector-icons/FontAwesome";
import * as ImagePicker from "expo-image-picker";
import { LinearProgress } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import { Picker } from "@react-native-picker/picker";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import {
  ADVERT_TYPE_PRODUCT,
  ADVERT_TYPE_SERVICE,
} from "../../constants/constantValues";
import { router, useLocalSearchParams } from "expo-router";

const UpdateAdvert = () => {
  const item = useLocalSearchParams();
  const [productImages, setProductImages] = useState(
    //@ts-ignore
    item.images.split(",") || []
  );
  const [description, setDescription] = useState(item.description || "");
  const [productName, setProductName] = useState(item.name || "");
  const [phoneNumber, setPhoneNumber] = useState(item.phoneNumber || undefined);
  const [email, setEmail] = useState(item.email || "");
  const [location, setLocation] = useState(item.location || "");
  const [category, setCategory] = useState(item.category || "");
  const [companyName, setCompanyName] = useState(item.companyName || undefined);
  const [type, setType] = useState(item.type || "");
  const [price, setPrice] = useState(item.price || 0);
  const { setLoading, loading, token } = useAuth();

  const [isModalVisible, setIsModalVisible] = useState(false);
  const [categories, setCategories] = useState([]);
  const maxDescriptionLength = 100;

  const delay = useCallback((duration: any) => {
    return new Promise((resolve) => setTimeout(resolve, duration));
  }, []);

  const pickProductImages = async () => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== "granted") {
      ToastAndroid.show(
        "Permission to access media library denied!",
        ToastAndroid.SHORT
      );
      return;
    }

    let result = await ImagePicker.launchImageLibraryAsync({
      aspect: [4, 3],
      quality: 1,
      allowsMultipleSelection: true,
    });

    if (!result.canceled) {
      setProductImages(result.assets.map((asset) => asset.uri));
    } else {
      ToastAndroid.show("You did not select any images!", ToastAndroid.SHORT);
    }
  };

  const createService = async () => {
    if (
      !productName ||
      !location ||
      !phoneNumber ||
      !email ||
      !category ||
      !description ||
      productImages.length === 0
    ) {
      await delay(3000);
      setIsModalVisible(true);
      ToastAndroid.show(
        "Please fill all the fields and upload images.!",
        ToastAndroid.SHORT
      );
      return;
    }
    setLoading(true);
    const formData = new FormData();
    productImages.forEach((uri: any, index: any) => {
      //@ts-ignore
      formData.append("images", {
        uri,
        type: "image/jpeg",
        name: `image_${index}.jpg`,
      });
    });
    //@ts-ignore
    formData.append("name", productName);
    //@ts-ignore
    formData.append("description", description);
    //@ts-ignore
    formData.append("location", location);
    //@ts-ignore
    formData.append("type", type);
    //@ts-ignore
    formData.append("email", email);
    if (type === ADVERT_TYPE_SERVICE) {
      //@ts-ignore
      formData.append("price", 0);
    } else {
      //@ts-ignore
      formData.append("price", +price);
    }
    //@ts-ignore
    formData.append("phoneNumber", phoneNumber);
    //@ts-ignore
    formData.append("category", category);
    //@ts-ignore
    formData.append("companyName", companyName);
    try {
      const response = await axios.put(
        `${baseUrl}/products/${item.id}`,
        formData,
        {
          headers: {
            Authorization: token,
            "Content-Type": "multipart/form-data",
          },
        }
      );
      if (response) {
        setLoading(false);
        setIsModalVisible(true);
        ToastAndroid.show(
          "The Advert was updated successfully.!",
          ToastAndroid.SHORT
        );
        await delay(2000);
        setIsModalVisible(false);
        router.push("/myAdverts");
      }
    } catch (error: any) {
      setLoading(false);
      setIsModalVisible(true);
      ToastAndroid.show(
        `Error Creating Advert! ${
          error.response?.data?.message || "An error occurred"
        }`,
        ToastAndroid.SHORT
      );
      await delay(4000);
      setIsModalVisible(false);
    }
  };

  const resetForm = () => {
    setPhoneNumber("");
    setCategory("");
    setDescription("");
    setEmail("");
    setLocation("");
    setProductImages([]);
    setProductName("");
  };

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(`${baseUrl}/category?type=${type}`, {
          headers: {
            Authorization: `${token}`,
          },
        }); // Adjust the endpoint based on your API
        const fetchedCategories = response.data.results;
        setCategories(fetchedCategories);
      } catch (error: any) {
        alert(error?.response.data.message);
      }
    };
    if (token) {
      fetchCategories();
    }
  }, []);

  return (
    <ScrollView style={styles.cover}>
      <Modal visible={isModalVisible} style={styles.modal} transparent={true}>
        <TouchableOpacity
          onPress={() => {
            setIsModalVisible(false);
          }}
          style={styles.closeButton}
        >
          <Text style={styles.closeButtonText}>Close</Text>
        </TouchableOpacity>
      </Modal>

      <Text style={styles.label}>Enter Advert name</Text>
      <TextInput
        style={styles.input}
        //@ts-ignore
        value={productName}
        onChangeText={setProductName}
        autoCapitalize="none"
      />
      <Text style={styles.label}>Enter Location</Text>
      <TextInput
        style={styles.input}
        //@ts-ignore
        value={location}
        onChangeText={setLocation}
        autoCapitalize="none"
      />
      <Text style={styles.label}>Enter Valid phone number</Text>
      <TextInput
        style={styles.input}
        //@ts-ignore
        value={phoneNumber}
        onChangeText={setPhoneNumber}
        keyboardType="phone-pad"
        autoCapitalize="none"
      />
      <Text style={styles.label}>Enter Valid Email</Text>
      <TextInput
        style={styles.input}
        //@ts-ignore
        value={email}
        onChangeText={setEmail}
        keyboardType="email-address"
      />
      {type === ADVERT_TYPE_PRODUCT && (
        <>
          <Text style={styles.label}>Product Price</Text>
          <TextInput
            style={styles.input}
            value={String(price)}
            onChangeText={setPrice}
            keyboardType="numeric"
          />
        </>
      )}
      <Text style={styles.label}>Company Name</Text>
      <TextInput
        style={styles.input}
        //@ts-ignore
        value={companyName}
        onChangeText={setCompanyName}
      />
      <Text style={styles.label}>Advert Type</Text>
      <Picker
        style={styles.input}
        selectedValue={type}
        onValueChange={(itemValue) => setType(itemValue)}
      >
        <Picker.Item label={"Select Advert Type"} value={""} />
        <Picker.Item label={ADVERT_TYPE_PRODUCT} value={ADVERT_TYPE_PRODUCT} />
        <Picker.Item label={ADVERT_TYPE_SERVICE} value={ADVERT_TYPE_SERVICE} />
      </Picker>
      <Text style={styles.label}>Advert Category</Text>
      <Picker
        style={styles.input}
        selectedValue={category}
        onValueChange={(itemValue) => setCategory(itemValue)}
      >
        <Picker.Item label={"Select Category"} value={""} />
        {categories.map((item, index) => (
          //@ts-ignore
          <Picker.Item key={index} label={item.title} value={item.id} />
        ))}
      </Picker>

      <Text style={styles.label}>Upload Quality service images</Text>
      <View style={styles.multipleImageContainer}>
        <TouchableOpacity onPress={pickProductImages}>
          <Icon name="upload" size={25} color="#aaa" />
        </TouchableOpacity>
      </View>

      <View style={styles.imageContainer}>
        {productImages &&
          productImages.map((uri: any, index: number) => (
            <Image key={index} source={{ uri }} style={styles.uploadedImage} />
          ))}
      </View>
      <View style={styles.textAreaContainer}>
        <TextInput
          style={styles.textArea}
          placeholder="Description"
          placeholderTextColor="grey"
          numberOfLines={10}
          multiline={true}
          maxLength={maxDescriptionLength}
          //@ts-ignore
          value={description}
          onChangeText={setDescription}
        />
        <Text style={styles.charCount}>
          {description.length}/{maxDescriptionLength}
        </Text>
      </View>
      {loading && (
        <View style={styles.progressContainer}>
          <LinearProgress color={COLORS.primary} />
          <Text style={styles.loadingText}>Submitting...</Text>
        </View>
      )}
      <View style={{ alignItems: "center" }}>
        <TouchableOpacity
          style={[styles.button, loading && styles.buttonLoading]}
          onPress={createService}
          disabled={loading}
        >
          <Text style={styles.buttonText}>Continue</Text>
        </TouchableOpacity>
      </View>
    </ScrollView>
  );
};

export default UpdateAdvert;

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
    marginBottom: 30,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  cover: {
    top: 10,
    marginBottom: 15,
    backgroundColor: COLORS.white,
  },
  label: {
    marginHorizontal: SIZES.width * 0.05,
  },
  priceRangeContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginBottom: 15,
  },
  priceInput: {
    flex: 1,
  },
  priceSeparator: {
    marginHorizontal: 10,
  },
  multipleImageContainer: {
    alignItems: "center",
    backgroundColor: COLORS.gray,
    width: SIZES.width * 0.9,
    height: 0.285407725 * SIZES.height,
    marginHorizontal: SIZES.width * 0.05,
    justifyContent: "center",
    marginBottom: 20,
  },
  uploadedImage: {
    width: 100,
    height: 100,
    resizeMode: "contain",
    marginHorizontal: SIZES.width * 0.05,
  },
  imageContainer: {
    flexDirection: "row",
    flexWrap: "wrap",
    justifyContent: "center",
  },
  textAreaContainer: {
    width: SIZES.width * 0.9,
    borderColor: "gray",
    borderWidth: 1,
    borderRadius: 5,
    padding: 10,
    backgroundColor: "#f9f9f9",
    marginHorizontal: SIZES.width * 0.05,
    marginBottom: 15,
  },
  textArea: {
    height: 150,
    justifyContent: "flex-start",
    textAlignVertical: "top",
  },
  charCount: {
    textAlign: "right",
    color: "gray",
    fontSize: 12,
  },
  progressContainer: {
    marginTop: 20,
    alignItems: "center",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 16,
  },
  buttonLoading: {
    backgroundColor: "#d4ba92", // Change to your desired color when loading
  },
  dropdown: {
    width: "90%",
    height: 50,
    borderColor: "#ccc",
    borderWidth: 1,
    borderRadius: 5,
    marginHorizontal: SIZES.width * 0.05,
    marginBottom: 15,
  },
  dropdownContainer: {
    borderColor: "#ccc",
    borderWidth: 1,
  },
  icon: {
    width: 30,
    height: 30,
    borderRadius: 15,
  },
  placeholderIcon: {
    width: 30,
    height: 30,
    borderRadius: 15,
    justifyContent: "center",
    alignItems: "center",
    backgroundColor: "#ccc",
  },
  modal: {
    justifyContent: "center",
    alignContent: "center",
    alignItems: "center",
    width: "50%",
    height: "55%",
  },
  closeButton: {
    justifyContent: "center",
    alignItems: "center",
    padding: 10,
    backgroundColor: COLORS.primary,
    borderRadius: 5,
  },
  closeButtonText: {
    color: "#fff",
    fontSize: 16,
  },
});
