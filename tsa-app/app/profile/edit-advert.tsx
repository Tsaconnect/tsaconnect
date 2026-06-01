// app/profile/edit-advert.tsx
//
// Edit Advert — prefill an existing product/service advert, edit its fields,
// and PUT the update back to the marketplace API.
//
// Restyled onto the TSA Signature design system (@/theme + @/components/ui).
// Visual/structural only: the advert prefill (seeded from the route params),
// field validation, image-picker behavior, the multipart `axios.put
// ${baseUrl}/products/:id` update submit, ToastAndroid feedback, and the
// post-update navigation to /myAdverts are all preserved verbatim.
import { Image, ScrollView, ToastAndroid, View } from "react-native";
import React, { useState, useEffect, useCallback } from "react";
import { SafeAreaView } from "react-native-safe-area-context";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import {
  ADVERT_TYPE_PRODUCT,
  ADVERT_TYPE_SERVICE,
} from "../../constants/constantValues";
import { router, useLocalSearchParams } from "expo-router";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  Icon,
  Input,
  PressableScale,
  SelectRow,
  PickerSheet,
  Spinner,
  Text,
  type PickerItem,
} from "@/components/ui";

const UpdateAdvert = () => {
  const { colors, spacing, radius } = useTheme();
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

  const [categories, setCategories] = useState([]);
  // Local picker-sheet visibility (replaces the legacy @react-native-picker
  // dropdowns; the underlying type/category state is unchanged).
  const [typeSheetVisible, setTypeSheetVisible] = useState(false);
  const [categorySheetVisible, setCategorySheetVisible] = useState(false);
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
        ToastAndroid.show(
          "The Advert was updated successfully.!",
          ToastAndroid.SHORT
        );
        await delay(2000);
        router.push("/myAdverts");
      }
    } catch (error: any) {
      setLoading(false);
      ToastAndroid.show(
        `Error Creating Advert! ${
          error.response?.data?.message || "An error occurred"
        }`,
        ToastAndroid.SHORT
      );
      await delay(4000);
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

  // ── Picker-sheet item maps (visual control only; setters unchanged) ──────
  const typeItems: PickerItem[] = [
    { id: ADVERT_TYPE_PRODUCT, label: ADVERT_TYPE_PRODUCT },
    { id: ADVERT_TYPE_SERVICE, label: ADVERT_TYPE_SERVICE },
  ];
  const categoryItems: PickerItem[] = categories.map((c: any) => ({
    id: c.id,
    label: c.title,
  }));
  const selectedCategoryLabel = categoryItems.find(
    //@ts-ignore
    (c) => c.id === category
  )?.label;

  return (
    <SafeAreaView
      style={{ flex: 1, backgroundColor: colors.background }}
      edges={["top"]}
    >
      <ScrollView
        contentContainerStyle={{
          padding: spacing.lg,
          paddingBottom: spacing["4xl"],
          gap: spacing.lg,
        }}
        keyboardShouldPersistTaps="handled"
      >
        <Input
          label="Advert name"
          //@ts-ignore
          value={productName}
          onChangeText={setProductName}
          autoCapitalize="none"
        />

        <Input
          label="Location"
          //@ts-ignore
          value={location}
          onChangeText={setLocation}
          autoCapitalize="none"
        />

        <Input
          label="Phone number"
          //@ts-ignore
          value={phoneNumber}
          onChangeText={setPhoneNumber}
          keyboardType="phone-pad"
          autoCapitalize="none"
        />

        <Input
          label="Email"
          //@ts-ignore
          value={email}
          onChangeText={setEmail}
          keyboardType="email-address"
        />

        {type === ADVERT_TYPE_PRODUCT && (
          <Input
            label="Product price"
            value={String(price)}
            onChangeText={setPrice}
            keyboardType="numeric"
          />
        )}

        <Input
          label="Company name"
          //@ts-ignore
          value={companyName}
          onChangeText={setCompanyName}
        />

        <SelectRow
          label="Advert type"
          //@ts-ignore
          value={type || undefined}
          placeholder="Select Advert Type"
          onPress={() => setTypeSheetVisible(true)}
        />

        <SelectRow
          label="Advert category"
          value={selectedCategoryLabel}
          placeholder="Select Category"
          onPress={() => setCategorySheetVisible(true)}
        />

        {/* ── Images ──────────────────────────────────────────────────── */}
        <View style={{ gap: spacing.sm }}>
          <Text variant="bodySm" weight="600" color="textSecondary">
            Quality service images
          </Text>
          <PressableScale
            onPress={pickProductImages}
            accessibilityRole="button"
            accessibilityLabel="Upload images"
            style={{
              minHeight: 120,
              borderRadius: radius.lg,
              borderWidth: 1.5,
              borderStyle: "dashed",
              borderColor: colors.border,
              backgroundColor: colors.surfaceAlt,
              alignItems: "center",
              justifyContent: "center",
              gap: spacing.xs,
              paddingVertical: spacing.xl,
            }}
          >
            <Icon name="upload" size={26} color={colors.textMuted} />
            <Text variant="caption" color="textMuted">
              Tap to upload images
            </Text>
          </PressableScale>

          {productImages && productImages.length > 0 && (
            <View
              style={{ flexDirection: "row", flexWrap: "wrap", gap: spacing.sm }}
            >
              {productImages.map((uri: any, index: number) => (
                <Image
                  key={index}
                  source={{ uri }}
                  style={{
                    width: 96,
                    height: 96,
                    borderRadius: radius.md,
                    backgroundColor: colors.surfaceAlt,
                  }}
                />
              ))}
            </View>
          )}
        </View>

        {/* ── Description ─────────────────────────────────────────────── */}
        <Input
          label="Description"
          placeholder="Description"
          multiline
          numberOfLines={10}
          maxLength={maxDescriptionLength}
          counter={maxDescriptionLength}
          //@ts-ignore
          value={description}
          onChangeText={setDescription}
          inputStyle={{ minHeight: 120, textAlignVertical: "top" }}
        />

        {loading && (
          <Card padding="md" style={{ alignItems: "center", gap: spacing.sm }}>
            <Spinner inline label="Submitting…" />
          </Card>
        )}

        <Button
          title="Continue"
          onPress={createService}
          disabled={loading}
          loading={loading}
          size="lg"
        />
      </ScrollView>

      <PickerSheet
        visible={typeSheetVisible}
        onClose={() => setTypeSheetVisible(false)}
        title="Select Advert Type"
        items={typeItems}
        //@ts-ignore
        selectedId={type || undefined}
        searchable={false}
        onSelect={(it) => {
          setType(it.id);
          setTypeSheetVisible(false);
        }}
      />

      <PickerSheet
        visible={categorySheetVisible}
        onClose={() => setCategorySheetVisible(false)}
        title="Select Category"
        items={categoryItems}
        //@ts-ignore
        selectedId={category || undefined}
        emptyText="No categories"
        onSelect={(it) => {
          setCategory(it.id);
          setCategorySheetVisible(false);
        }}
      />
    </SafeAreaView>
  );
};

export default UpdateAdvert;
