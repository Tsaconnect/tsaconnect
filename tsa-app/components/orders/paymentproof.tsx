// components/orders/paymentproof.tsx
//
// Payment-proof uploader — pick a proof-of-payment image and POST it to the
// order pay endpoint. Restyled onto the TSA Signature design system (theme
// tokens + @/components/ui primitives) off the legacy COLORS/SIZES chrome.
//
// The image upload + submit flow is preserved EXACTLY: same expo-image-picker
// media-library pick (local URI), same multipart FormData, same
// `axios.post(${baseUrl}/order/${id}/pay)` with the same headers + interceptor,
// same ToastAndroid success + router.push('/confirmation'), and the same
// alert()-based validation/permission/error paths. Visual restyle only.
import { Image, View, ToastAndroid } from "react-native";
import React, { useState } from "react";
import { generateFileName } from "../../constants/api/filename";
import { sendProof } from "../../constants/api/AuthenticationService";
import * as ImagePicker from "expo-image-picker";
import { router } from "expo-router";
import { apiClient, baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { useAuth } from "../../AuthContext/AuthContext";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  Icon,
  PressableScale,
  Text,
} from "@/components/ui";

//@ts-ignore
const PaymentProof = ({ id }) => {
  const { colors, spacing, radius } = useTheme();
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
    <View style={{ gap: spacing.lg }}>
      <View style={{ gap: spacing.xs }}>
        <Text variant="title3" weight="700">
          Upload Proof of Payment
        </Text>
        <Text variant="bodySm" color="textSecondary">
          Attach a screenshot or photo of your payment receipt, then send it for review.
        </Text>
      </View>

      {image ? (
        <Card padding="md" style={{ gap: spacing.md }}>
          <Image
            source={{ uri: image }}
            style={{
              width: "100%",
              height: 220,
              borderRadius: radius.md,
              resizeMode: "contain",
              backgroundColor: colors.surfaceAlt,
            }}
          />
          <PressableScale
            onPress={pickImageAsync}
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "center",
              gap: spacing.xs,
              paddingVertical: spacing.sm,
            }}
          >
            <Icon name="refresh" size={16} color={colors.accent} />
            <Text variant="bodySm" weight="600" color="accent">
              Replace image
            </Text>
          </PressableScale>
        </Card>
      ) : (
        <PressableScale onPress={pickImageAsync}>
          <View
            style={{
              alignItems: "center",
              justifyContent: "center",
              gap: spacing.sm,
              height: 160,
              borderWidth: 1,
              borderStyle: "dashed",
              borderColor: colors.border,
              borderRadius: radius.lg,
              backgroundColor: colors.surfaceAlt,
            }}
          >
            <Icon name="upload" size={28} color={colors.textMuted} />
            <Text variant="bodySm" weight="600" color="textSecondary">
              Tap to select an image
            </Text>
          </View>
        </PressableScale>
      )}

      <Button title="Send proof" leftIcon="send" onPress={uploadProof} />
    </View>
  );
};

export default PaymentProof;
