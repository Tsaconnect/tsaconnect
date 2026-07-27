// components/category/EditCategory.tsx
//
// Edit-category form (admin). Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives) off the legacy COLORS/SIZES
// styling. This is a VISUAL/STRUCTURAL restyle only — all data fetching
// (axios GET /category?isParent=true, PATCH /category/:id multipart), the
// useLocalSearchParams prefill, the title/type validation, the expo-image-picker
// flow, FormData assembly, and the post-update navigation are preserved verbatim.
import React, { useState, useEffect } from "react";
import { View, ScrollView, Image, Alert } from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
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
import {
  BOOKING_TIERS,
  BOOKING_TIER_LABELS,
  getCategoryBookingFees,
  upsertCategoryBookingFees,
  type BookingFees,
} from "../../services/bookingFeeApi";

const TYPE_OPTIONS: PickerItem[] = [
  { id: "Product", label: "Product" },
  { id: "Service", label: "Service" },
  // "Both" lets a same-titled category serve both marketplace sections without
  // listings leaking across them (the type filter keeps them separated).
  { id: "Both", label: "Both (Product & Service)" },
];

// Service fee mode picker (shown only when type is Service or Both).
const FEE_MODE_OPTIONS: PickerItem[] = [
  { id: "standard", label: "Standard (merchant sets price + call-out)" },
  { id: "booking", label: "Booking (admin sets merged per-area fare)" },
];

// Service-type picker (shown only when type is Service or Both). Determines
// how customers engage a listing in this category.
type ServiceType = "contact" | "order" | "booking";
const SERVICE_TYPE_OPTIONS: PickerItem[] = [
  { id: "contact", label: "Contact (pay to unlock & engage)" },
  { id: "order", label: "Order (select menu items)" },
  { id: "booking", label: "Booking (reserve a date/time)" },
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

  // Service-pricing config (only meaningful for Service/Both categories).
  const [serviceFeeMode, setServiceFeeMode] = useState<"standard" | "booking">(
    item.serviceFeeMode === "booking" ? "booking" : "standard"
  );
  const [feeModeSheetVisible, setFeeModeSheetVisible] = useState(false);
  // Service type — prefilled from the category, defaults to "contact".
  const [serviceType, setServiceType] = useState<ServiceType>(
    item.serviceType === "order" || item.serviceType === "booking"
      ? item.serviceType
      : "contact"
  );
  const [serviceTypeSheetVisible, setServiceTypeSheetVisible] = useState(false);
  // Per-tier booking fares (USD), keyed by BOOKING_TIERS — raw input strings.
  const [bookingFares, setBookingFares] = useState<Record<string, string>>({});

  const isServiceType = type === "Service" || type === "Both";
  const showFareEditor = isServiceType && serviceFeeMode === "booking";

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

  // Prefill the booking fare editor for a booking-mode service category.
  useEffect(() => {
    if (!item.id || serviceFeeMode !== "booking") return;
    let cancelled = false;
    (async () => {
      const res = await getCategoryBookingFees(String(item.id));
      if (cancelled || !res.success || !res.data?.fees) return;
      const prefilled: Record<string, string> = {};
      for (const tier of BOOKING_TIERS) {
        const amount = res.data.fees[tier];
        if (amount != null && amount !== 0) prefilled[tier] = String(amount);
      }
      setBookingFares(prefilled);
    })();
    return () => {
      cancelled = true;
    };
    // Re-fetch only when the category or its mode changes.
  }, [item.id, serviceFeeMode]);

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
    // Service-fee mode & service type only apply to Service/Both categories.
    if (isServiceType) {
      //@ts-ignore
      formData.append("serviceFeeMode", serviceFeeMode);
      //@ts-ignore
      formData.append("serviceType", serviceType);
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
        // For a booking-mode service category, persist the per-tier fare
        // schedule against the category.
        if (isServiceType && serviceFeeMode === "booking" && item.id) {
          const fees: BookingFees = {};
          for (const tier of BOOKING_TIERS) {
            fees[tier] = Number(bookingFares[tier]) || 0;
          }
          await upsertCategoryBookingFees(String(item.id), fees);
        }
        Alert.alert("success", "The category was updated successfully.");
        //@ts-ignore
        router.push("/categories");
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
  const selectedFeeModeLabel = FEE_MODE_OPTIONS.find(
    (o) => o.id === serviceFeeMode
  )?.label;
  const selectedServiceTypeLabel = SERVICE_TYPE_OPTIONS.find(
    (o) => o.id === serviceType
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

          {/* Service type — only for Service / Both categories */}
          {isServiceType && (
            <SelectRow
              label="Service type"
              placeholder="Select service type"
              value={selectedServiceTypeLabel}
              onPress={() => setServiceTypeSheetVisible(true)}
            />
          )}

          {/* Service fee mode — only for Service / Both categories */}
          {isServiceType && (
            <SelectRow
              label="Service fee mode"
              placeholder="Select fee mode"
              value={selectedFeeModeLabel}
              onPress={() => setFeeModeSheetVisible(true)}
            />
          )}

          {/* Booking fare editor — one USD input per area tier */}
          {showFareEditor && (
            <View style={{ gap: spacing.md }}>
              <View style={{ gap: spacing.xs }}>
                <Text variant="bodyMed" weight="600">
                  Booking fares (USD)
                </Text>
                <Text variant="caption" color="textMuted">
                  Set the merged call-out + booking fee per area tier. Blank
                  counts as 0.
                </Text>
              </View>
              {BOOKING_TIERS.map((tier) => (
                <Input
                  key={tier}
                  label={BOOKING_TIER_LABELS[tier]}
                  value={bookingFares[tier] ?? ""}
                  onChangeText={(v) =>
                    setBookingFares((prev) => ({ ...prev, [tier]: v }))
                  }
                  keyboardType="numeric"
                  placeholder="0.00"
                />
              ))}
            </View>
          )}

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

      {/* Service-type picker */}
      <PickerSheet
        visible={serviceTypeSheetVisible}
        onClose={() => setServiceTypeSheetVisible(false)}
        title="Service type"
        items={SERVICE_TYPE_OPTIONS}
        selectedId={serviceType}
        searchable={false}
        onSelect={(it) => {
          setServiceType(it.id as ServiceType);
          setServiceTypeSheetVisible(false);
        }}
      />

      {/* Service-fee-mode picker */}
      <PickerSheet
        visible={feeModeSheetVisible}
        onClose={() => setFeeModeSheetVisible(false)}
        title="Service fee mode"
        items={FEE_MODE_OPTIONS}
        selectedId={serviceFeeMode}
        searchable={false}
        onSelect={(it) => {
          setServiceFeeMode(it.id as "standard" | "booking");
          setFeeModeSheetVisible(false);
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
