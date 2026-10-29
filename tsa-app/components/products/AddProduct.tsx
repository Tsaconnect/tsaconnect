// components/products/AddProduct.tsx
//
// Add-product form (merchant lists a marketplace product). Restyled onto the
// TSA Signature design system (@/theme + @/components/ui). VISUAL/STRUCTURAL
// only — the categories fetch, expo-image-picker local-URI selection, multipart
// FormData submit (axios → ${baseUrl}/products), validation, attribute editing,
// and form reset are all unchanged from the legacy version.
import {
  Image,
  ScrollView,
  View,
  Alert,
  Platform,
  KeyboardAvoidingView,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import React, { useState, useEffect, useCallback } from "react";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import { useTheme } from "@/theme";
import PromotionFields from "@/components/promotion/PromotionFields";
import { usePromotion } from "@/components/promotion/promoForm";
import { SchemaFields } from "@/components/forms/SchemaFields";
import { useCategoryListingFields } from "@/hooks/useCategoryListingFields";
import { cleanSchemaAnswers, validateSchemaAnswers } from "@/utils/schemaFields";
import { categoryPathLabels } from "@/utils/categoryTree";
import {
  Button,
  Card,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  SelectRow,
  Spinner,
  Text,
  PickerSheet,
  type PickerItem,
} from "@/components/ui";

const AddProduct = () => {
  const { colors, spacing, radius } = useTheme();
  const { token } = useAuth();
  const [productImages, setProductImages] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [productName, setProductName] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [email, setEmail] = useState("");
  const [location, setLocation] = useState("");
  const [category, setCategory] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [price, setPrice] = useState("");
  const [stock, setStock] = useState("");
  const promotion = usePromotion();
  const [promoError, setPromoError] = useState<string | null>(null);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const { setLoading, loading } = useAuth();
  const [categories, setCategories] = useState<{ id: string; title: string; parentCategory?: string }[]>([]);
  // Answers to the chosen category's listing fields (brand, …).
  const [listingValues, setListingValues] = useState<Record<string, any>>({});
  const [listingErrors, setListingErrors] = useState<Record<string, string>>({});
  const { fields: listingFields } = useCategoryListingFields(category || null);
  const maxDescriptionLength = 500;
  // Sheet visibility for the category picker (replaces the inline RN Picker).
  const [categoryPickerOpen, setCategoryPickerOpen] = useState(false);

  const delay = useCallback((duration: number) => {
    return new Promise((resolve) => setTimeout(resolve, duration));
  }, []);

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(
          `${baseUrl}/products/category/all?type=Product&active=true`,
          { headers: { Authorization: token } }
        );
        const data = response.data.data;
        setCategories(Array.isArray(data) ? data : []);
      } catch (error) {
        // Categories will show empty picker
      }
    };
    if (token) fetchCategories();
  }, [token]);

  const pickProductImages = async () => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== "granted") {
      Alert.alert("Permission Required", "Please allow access to your photo library.");
      return;
    }

    let result = await ImagePicker.launchImageLibraryAsync({
      aspect: [4, 3],
      quality: 0.8,
      allowsMultipleSelection: true,
    });

    if (!result.canceled) {
      setProductImages(result.assets.map((asset) => asset.uri));
    }
  };

  const removeImage = (index: number) => {
    setProductImages(prev => prev.filter((_, i) => i !== index));
  };

  const createProduct = async () => {
    if (!productName || !location || !phoneNumber || !email || !category || !description || !price || productImages.length === 0) {
      Alert.alert("Missing Fields", "Please fill all required fields and upload at least one image.");
      return;
    }
    const invalidPromo = promotion.validate(price);
    setPromoError(invalidPromo);
    if (invalidPromo) {
      Alert.alert("Promotion", invalidPromo);
      return;
    }
    const listingProblems = validateSchemaAnswers(listingFields, listingValues);
    setListingErrors(listingProblems);
    if (Object.keys(listingProblems).length > 0) {
      Alert.alert("Missing details", "Please fill in the required category details.");
      return;
    }
    setLoading(true);
    const formData = new FormData();
    productImages.forEach((uri, index) => {
      //@ts-ignore
      formData.append("images", {
        uri,
        type: "image/jpeg",
        name: `image_${index}.jpg`,
      });
    });

    formData.append("name", productName);
    formData.append("description", description);
    formData.append("location", location);
    formData.append("type", "Product");
    formData.append("email", email);
    formData.append("phoneNumber", phoneNumber);
    formData.append("category", category);
    formData.append("companyName", companyName);
    if (listingFields.length > 0) {
      formData.append("listingValues", JSON.stringify(cleanSchemaAnswers(listingFields, listingValues)));
    }
    //@ts-ignore
    formData.append("price", parseInt(price));
    //@ts-ignore
    formData.append("stock", parseInt(stock || "0"));
    promotion.append(formData);
    attributes.forEach((attribute, index) => {
      formData.append(`attributes[${index}].name`, attribute.name);
      formData.append(`attributes[${index}].value`, attribute.value);
    });
    try {
      const response = await axios.post(`${baseUrl}/products`, formData, {
        headers: {
          Authorization: token,
          "Content-Type": "multipart/form-data",
        },
      });
      if (response) {
        setLoading(false);
        Alert.alert("Success", "Product created successfully!");
        resetForm();
      }
    } catch (error: any) {
      setLoading(false);
      Alert.alert("Error", error.response?.data?.message || "Failed to create product.");
    }
  };

  const resetForm = () => {
    setPhoneNumber("");
    setCategory("");
    setListingValues({});
    setListingErrors({});
    setDescription("");
    setEmail("");
    setLocation("");
    setProductImages([]);
    setProductName("");
    setPrice("");
    setStock("");
    setAttributes([]);
    setCompanyName("");
    promotion.reset();
    setPromoError(null);
  };

  const handleAddAttribute = () => {
    setAttributes([...attributes, { name: "", value: "" }]);
  };

  const handleRemoveAttribute = (index: number) => {
    setAttributes(prev => prev.filter((_, i) => i !== index));
  };

  const handleAttributeChange = (index: number, field: "name" | "value", value: string) => {
    const updated = [...attributes];
    updated[index][field] = value;
    setAttributes(updated);
  };

  const categoryItems: PickerItem[] = categoryPathLabels(categories).map((item) => ({
    id: item.id,
    label: item.path,
  }));
  const selectedCategoryLabel = categories.find((c) => c.id === category)?.title;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing["4xl"] }}
          keyboardShouldPersistTaps="handled"
          showsVerticalScrollIndicator={false}
        >
          {/* Header */}
          <Text variant="title1" weight="700">
            Add Product
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Fill in the details to list your product
          </Text>

          {/* Image Upload */}
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginTop: spacing.xl, marginBottom: spacing.sm }}>
            Photos
          </Text>
          <PressableScale onPress={pickProductImages}>
            <Card
              padding="2xl"
              style={{
                alignItems: "center",
                justifyContent: "center",
                borderWidth: 2,
                borderStyle: "dashed",
                borderColor: colors.accentRail,
                backgroundColor: colors.surfaceAlt,
              }}
            >
              <Icon name="camera" size={32} color={colors.accent} />
              <Text variant="bodyMed" weight="600" color="accent" style={{ marginTop: spacing.sm }}>
                Tap to upload images
              </Text>
              <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                High quality photos help sell faster
              </Text>
            </Card>
          </PressableScale>

          {productImages.length > 0 && (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              contentContainerStyle={{ gap: spacing.sm, paddingVertical: spacing.sm }}
            >
              {productImages.map((uri, index) => (
                <View key={index} style={{ position: "relative" }}>
                  <Image
                    source={{ uri }}
                    style={{
                      width: 80,
                      height: 80,
                      borderRadius: radius.md,
                      backgroundColor: colors.surfaceAlt,
                    }}
                  />
                  <PressableScale
                    onPress={() => removeImage(index)}
                    accessibilityRole="button"
                    accessibilityLabel="Remove image"
                    hitSlop={6}
                    style={{
                      position: "absolute",
                      top: -6,
                      insetInlineEnd: -6,
                      width: 24,
                      height: 24,
                      borderRadius: radius.pill,
                      alignItems: "center",
                      justifyContent: "center",
                      backgroundColor: colors.background,
                      borderWidth: 1,
                      borderColor: colors.border,
                    }}
                  >
                    <Icon name="close" size={14} color={colors.textPrimary} />
                  </PressableScale>
                </View>
              ))}
            </ScrollView>
          )}

          {/* Product Details */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing.xl, marginBottom: spacing.md }}>
            Product Details
          </Text>

          <Input
            label="Product Name *"
            value={productName}
            onChangeText={setProductName}
            placeholder="e.g. Organic Honey 500ml"
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <SelectRow
            label="Category *"
            value={selectedCategoryLabel}
            placeholder="Select a category"
            onPress={() => setCategoryPickerOpen(true)}
            style={{ marginBottom: spacing.lg }}
          />

          <View style={{ flexDirection: "row", gap: spacing.md, marginBottom: spacing.lg }}>
            <Input
              label="Price (USDT) *"
              value={price}
              onChangeText={setPrice}
              keyboardType="numeric"
              placeholder="0.00"
              containerStyle={{ flex: 1 }}
            />
            <Input
              label="Stock"
              value={stock}
              onChangeText={setStock}
              keyboardType="numeric"
              placeholder="0"
              containerStyle={{ flex: 1 }}
            />
          </View>

          <Input
            label="Description *"
            placeholder="Describe your product..."
            multiline
            numberOfLines={5}
            maxLength={maxDescriptionLength}
            counter
            value={description}
            onChangeText={setDescription}
            textAlignVertical="top"
            inputStyle={{ minHeight: 120 }}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          {/* Contact & Location */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing.lg, marginBottom: spacing.md }}>
            Contact &amp; Location
          </Text>

          <Input
            label="Company Name"
            value={companyName}
            onChangeText={setCompanyName}
            placeholder="Your business name"
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <Input
            label="Location *"
            value={location}
            onChangeText={setLocation}
            placeholder="City, State"
            leftAdornment={<Icon name="nearMe" size={20} color={colors.textSecondary} />}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <Input
            label="Phone Number *"
            value={phoneNumber}
            onChangeText={setPhoneNumber}
            keyboardType="phone-pad"
            placeholder="+234 800 000 0000"
            leftAdornment={<Icon name="mobile" size={20} color={colors.textSecondary} />}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <Input
            label="Email *"
            value={email}
            onChangeText={setEmail}
            keyboardType="email-address"
            autoCapitalize="none"
            placeholder="you@example.com"
            leftAdornment={<Icon name="send" size={20} color={colors.textSecondary} />}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <Text variant="title3" weight="600" style={{ marginTop: spacing.lg, marginBottom: spacing.md }}>
            Promotion
          </Text>
          <PromotionFields
            promotion={promotion}
            originalPrice={price}
            currencySymbol="$"
            error={promoError}
          />

          {/* The category's own listing fields (set by admins per category). */}
          {listingFields.length > 0 && (
            <>
              <Text variant="title3" weight="600" style={{ marginTop: spacing.lg, marginBottom: spacing.md }}>
                {selectedCategoryLabel ?? "Category"} details
              </Text>
              <SchemaFields
                fields={listingFields}
                answers={listingValues}
                errors={listingErrors}
                onChange={(key, value) => {
                  setListingValues((prev) => ({ ...prev, [key]: value }));
                  setListingErrors((prev) => (prev[key] ? { ...prev, [key]: "" } : prev));
                }}
              />
            </>
          )}

          {/* Attributes */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing.lg }}>
            Attributes
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.md }}>
            Add custom details like size, color, weight, etc.
          </Text>

          {attributes.map((attribute, index) => (
            <View
              key={index}
              style={{ flexDirection: "row", alignItems: "flex-end", gap: spacing.sm, marginBottom: spacing.md }}
            >
              <Input
                placeholder="Name"
                value={attribute.name}
                onChangeText={(value) => handleAttributeChange(index, "name", value)}
                containerStyle={{ flex: 1 }}
              />
              <Input
                placeholder="Value"
                value={attribute.value}
                onChangeText={(value) => handleAttributeChange(index, "value", value)}
                containerStyle={{ flex: 1 }}
              />
              <IconButton
                icon="trash"
                variant="plain"
                color={colors.danger}
                onPress={() => handleRemoveAttribute(index)}
                accessibilityLabel="Remove attribute"
              />
            </View>
          ))}

          <Button
            title="Add Attribute"
            leftIcon="plus"
            variant="secondary"
            onPress={handleAddAttribute}
          />

          {/* Submit */}
          {loading && (
            <View style={{ alignItems: "center", marginTop: spacing.lg, gap: spacing.sm }}>
              <Spinner />
              <Text variant="caption" color="textSecondary">
                Submitting...
              </Text>
            </View>
          )}

          <Button
            title="Publish Product"
            leftIcon="upload"
            onPress={createProduct}
            disabled={loading}
            loading={loading}
            style={{ marginTop: spacing.xl }}
          />
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={categoryPickerOpen}
        onClose={() => setCategoryPickerOpen(false)}
        title="Select a category"
        items={categoryItems}
        selectedId={category}
        onSelect={(item) => {
          setCategory(item.id);
          setCategoryPickerOpen(false);
        }}
        emptyText="No categories available"
      />
    </SafeAreaView>
  );
};

export default AddProduct;
