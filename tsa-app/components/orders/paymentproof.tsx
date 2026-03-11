import { Image, StyleSheet, Text, View, ToastAndroid } from "react-native";
import React, { useState } from "react";
import { TouchableOpacity } from "react-native";
import { generateFileName } from "../../constants/api/filename";
import { sendProof } from "../../constants/api/AuthenticationService";
import * as ImagePicker from "expo-image-picker";
import { COLORS, SIZES } from "../../constants";
import { router } from "expo-router";
import { Icon } from "react-native-elements";
import { apiClient, baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { useAuth } from "../../AuthContext/AuthContext";

//@ts-ignore
const PaymentProof = ({ id }) => {
  const { token } = useAuth();
  const [image, setImage] = useState(null);
  const pickImageAsync = async () => {
    try {
      const { status } =
        await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== "granted") {
        alert("Permission to access media library denied");
        return;
      }

      let result = await ImagePicker.launchImageLibraryAsync({
        allowsEditing: false,
        aspect: [4, 3],
        quality: 1,
      });

      if (!result.canceled) {
        //@ts-ignore
        setImage(result.assets[0].uri);
      } else {
        alert("You did not select any image.");
      }
    } catch (error) {
      console.error("Error picking image:", error);
    }
  };

  async function uploadProof() {
    if (!image) {
      alert("Please select Image");
      return;
    }
    const formData = new FormData();

    //@ts-ignore
    formData.append("proof", {
      uri: image,
      type: "image/jpeg",
      name: `image_${generateFileName}.jpg`,
    });
    try {
      apiClient.interceptors.request.use((config) => {
        config.headers["Content-Type"] = "multipart/form-data";
        config.headers.Accept = "application/json";
        return config;
      });
      const response = await axios.post(
        `${baseUrl}/order/${id}/pay`,
        formData,
        {
          headers: {
            Authorization: token,
            "Content-Type": "multipart/form-data",
          },
        }
      );
      if (response.status === 200) {
        ToastAndroid.show(
          "The payment was created successfully!",
          ToastAndroid.SHORT
        );
        router.push("/confirmation");
      }
    } catch (error: any) {
      alert(error?.response.data.message);
    }
  }
  return (
    <View style={styles.container}>
      <Text style={styles.label}>Upload Proof of Payment</Text>
      <View style={styles.uploadContainer}>
        <TouchableOpacity onPress={pickImageAsync}>
          <Icon name="upload" size={25} color="#aaa" />
        </TouchableOpacity>
      </View>

      {image && (
        <View style={styles.imageContainer}>
          <Image source={{ uri: image }} style={styles.uploadedImage} />
        </View>
      )}
      <View style={{ alignItems: "center", marginBottom: 55 }}>
        <TouchableOpacity style={styles.button} onPress={uploadProof}>
          <Text style={styles.buttonText}>Send proof</Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default PaymentProof;

const styles = StyleSheet.create({
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
  container: {
    justifyContent: "center",
    padding: 20,
    backgroundColor: COLORS.white,
    alignItems: "center",
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
});
