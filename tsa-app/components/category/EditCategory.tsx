// components/category/EditCategory.tsx
//
// Edit-category form (admin). Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives) off the legacy COLORS/SIZES
// styling. This is a VISUAL/STRUCTURAL restyle only — all data fetching
// (axios GET /category?isParent=true, PATCH /category/:id multipart), the
// useLocalSearchParams prefill, the title/type validation, the expo-image-picker
// flow, FormData assembly, and the post-update navigation are preserved verbatim.
import React, { useState, useEffect } from "react";
import { View, ScrollView, Image } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { Alert } from "react-native";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import { router, useLocalSearchParams } from "expo-router";
import axios from "axios";
import { generateFileName } from "../../constants/api/filename";
import { baseUrl } from "../../constants/api/apiClient";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  Icon,
  Input,
  PressableScale,
  SelectRow,
  PickerSheet,
  Text,
  Toggle,
  type PickerItem,
} from "@/components/ui";

const TYPE_OPTIONS: PickerItem[] = [
  { id: "Product", label: "Product" },
  { id: "Service", label: "Service" },
];

const EditCategoryScreen = () => {
  const { colors, spacing, radius } = useTheme();
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

  // Picker-sheet visibility (presentation only — selection still drives the
  // same `type` / `parentCategory` state used by updateCategory's FormData).
  const [typeSheetVisible, setTypeSheetVisible] = useState(false);
  const [parentSheetVisible, setParentSheetVisible] = useState(false);

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

  // Parent-category options for the picker sheet (label = title, id = category id).
  const parentItems: PickerItem[] = parentCategories.map((c: any) => ({
    id: c.id,
    label: c.title,
  }));
  const selectedParentLabel = parentItems.find(
    (p) => p.id === parentCategory
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
        }}
        keyboardShouldPersistTaps="handled"
      >
        <Card padding="lg" style={{ gap: spacing.lg }}>
          <Input
            label="Title"
            //@ts-ignore
            value={title}
            onChangeText={(e) => setTitle(e)}
          />

          <Input
            label="Description"
            //@ts-ignore
            value={description}
            onChangeText={(e) => setDescription(e)}
            autoCapitalize="none"
          />

          <SelectRow
            label="Category Type"
            placeholder="Select Type"
            value={type ? String(type) : undefined}
            onPress={() => setTypeSheetVisible(true)}
          />

          {/* Featured image */}
          <View style={{ gap: spacing.sm }}>
            <Text variant="bodySm" weight="600" color="textSecondary">
              Featured Image
            </Text>
            <PressableScale
              onPress={pickImageAsync}
              accessibilityRole="button"
              accessibilityLabel="Upload featured image"
              style={{
                alignItems: "center",
                justifyContent: "center",
                height: 120,
                borderRadius: radius.md,
                borderWidth: 1,
                borderColor: colors.hairline,
                borderStyle: "dashed",
                backgroundColor: colors.surfaceAlt,
              }}
            >
              <Icon name="upload" size={26} color={colors.textMuted} />
              <Text
                variant="caption"
                color="textMuted"
                style={{ marginTop: spacing.xs }}
              >
                Tap to upload
              </Text>
            </PressableScale>

            {featuredImage && (
              <View style={{ alignItems: "center", marginTop: spacing.xs }}>
                <Image
                  //@ts-ignore
                  source={{ uri: featuredImage }}
                  style={{
                    width: 120,
                    height: 120,
                    resizeMode: "contain",
                    borderRadius: radius.md,
                  }}
                />
              </View>
            )}
          </View>
        </Card>

        {/* Options */}
        <Card padding="lg" style={{ marginTop: spacing.lg, gap: spacing.lg }}>
          {/* Select parent category */}
          <View style={{ gap: spacing.sm }}>
            <View
              style={{
                flexDirection: "row",
                alignItems: "center",
                justifyContent: "space-between",
                gap: spacing.md,
              }}
            >
              <View style={{ flex: 1 }}>
                <Text variant="bodyMed" weight="600">
                  Select Parent Category
                </Text>
                <Text variant="caption" color="textMuted">
                  Nest this category under a parent
                </Text>
              </View>
              <Toggle
                value={!!hasParent}
                onValueChange={() => setHasParent(!hasParent)}
                accessibilityLabel="Select parent category"
              />
            </View>

            {hasParent && (
              <SelectRow
                placeholder="Select Parent Category"
                value={selectedParentLabel}
                onPress={() => setParentSheetVisible(true)}
              />
            )}
          </View>

          {/* Is parent */}
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              gap: spacing.md,
            }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="600">
                Parent Category?
              </Text>
              <Text variant="caption" color="textMuted">
                Mark this as a top-level parent category
              </Text>
            </View>
            <Toggle
              value={!!isParent}
              onValueChange={() => setIsParent(!isParent)}
              accessibilityLabel="Is parent"
            />
          </View>

          {/* Featured */}
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              gap: spacing.md,
            }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="bodyMed" weight="600">
                Featured?
              </Text>
              <Text variant="caption" color="textMuted">
                Highlight this category in featured listings
              </Text>
            </View>
            <Toggle
              value={!!isFeatured}
              onValueChange={() => setIsFeatured(!isFeatured)}
              accessibilityLabel="Is featured"
            />
          </View>
        </Card>

        <Button
          title="Update Category"
          onPress={updateCategory}
          disabled={loading}
          loading={loading}
          size="lg"
          style={{ marginTop: spacing.xl }}
        />
      </ScrollView>

      {/* Category-type picker */}
      <PickerSheet
        visible={typeSheetVisible}
        onClose={() => setTypeSheetVisible(false)}
        title="Select Type"
        items={TYPE_OPTIONS}
        selectedId={type ? String(type) : undefined}
        searchable={false}
        onSelect={(it) => {
          setType(it.id);
          setTypeSheetVisible(false);
        }}
      />

      {/* Parent-category picker */}
      <PickerSheet
        visible={parentSheetVisible}
        onClose={() => setParentSheetVisible(false)}
        title="Select Parent Category"
        items={parentItems}
        selectedId={parentCategory ? String(parentCategory) : undefined}
        onSelect={(it) => {
          setParentCategory(it.id);
          setParentSheetVisible(false);
        }}
      />
    </SafeAreaView>
  );
};

export default EditCategoryScreen;
