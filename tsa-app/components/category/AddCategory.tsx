// components/category/AddCategory.tsx
//
// Add-category form (admin). Migrated onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives). Behavior is unchanged: the
// parent-category fetch (axios GET /category?isParent=true), the expo-image-picker
// featured-image flow, the Title+Type required validation, the multipart FormData
// build, the registerCategory submit, the success Alert + form reset, and the
// default export are all preserved verbatim — this is a visual/structural
// restyle only. The two @react-native-picker dropdowns are re-expressed as
// SelectRow + PickerSheet, and the three FontAwesome checkboxes as themed
// Toggle rows, with no change to the underlying state they drive.
import { Alert, Image, ScrollView, View } from "react-native";
import React, { useEffect, useState } from "react";
import { SafeAreaView } from "react-native-safe-area-context";
import * as ImagePicker from "expo-image-picker";
import { registerCategory } from "../../constants/api/AuthenticationService";
import { useAuth } from "../../AuthContext/AuthContext";
import { generateFileName } from "../../constants/api/filename";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  Icon,
  Input,
  PickerSheet,
  PressableScale,
  SelectRow,
  Text,
  Toggle,
  type PickerItem,
} from "@/components/ui";
import {
  BOOKING_TIERS,
  BOOKING_TIER_LABELS,
  upsertCategoryBookingFees,
  type BookingFees,
} from "../../services/bookingFeeApi";

const TYPE_OPTIONS: PickerItem[] = [
  { id: "Product", label: "Product" },
  { id: "Service", label: "Service" },
  // "Both" lets a same-titled category serve both sections (e.g. "Generators"
  // for a generator product AND a generator-repair service). Listings stay in
  // their own marketplace section — the type filter prevents cross-leakage.
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

const AddCategoryScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const [description, setDescription] = useState("");
  const [title, setTitle] = useState("");
  const [type, setType] = useState("");
  const [featuredImage, setFeaturedImage] = useState(null);
  const [parentCategory, setParentCategory] = useState("");
  const [isFeatured, setIsFeatured] = useState(false);
  const [isParent, setIsParent] = useState(false);
  const [hasParent, setHasParent] = useState(false);
  const [parentCategories, setParentCategories] = useState([]);
  const { token, currentUser } = useAuth();
  const [loading, setLoading] = useState(false);

  // Sheet visibility for the SelectRow-backed pickers (UI-only state).
  const [typeSheetOpen, setTypeSheetOpen] = useState(false);
  const [parentSheetOpen, setParentSheetOpen] = useState(false);

  // Service-pricing config (only meaningful for Service/Both categories).
  const [serviceFeeMode, setServiceFeeMode] = useState<"standard" | "booking">(
    "standard"
  );
  const [feeModeSheetOpen, setFeeModeSheetOpen] = useState(false);
  // Service type — how customers engage listings in this category.
  const [serviceType, setServiceType] = useState<ServiceType>("contact");
  const [serviceTypeSheetOpen, setServiceTypeSheetOpen] = useState(false);
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
        setFeaturedImage(result.assets[0].uri);
      } else {
        alert("You did not select any image.");
      }
    } catch (error) {
      console.error("Error picking image:", error);
    }
  };

  const createCategory = async () => {
    if (!title || !type) {
      Alert.alert("error", "Title and type are required");
      return;
    }
    const formData = new FormData();
    if (featuredImage) {
      //@ts-ignore
      formData.append("image", {
        uri: featuredImage,
        type: "image/jpeg",
        name: `image_${generateFileName()}.jpg`,
      });
    }

    formData.append("title", title);
    formData.append("description", description);
    formData.append("type", type);
    //@ts-ignore
    formData.append("isFeatured", isFeatured);
    //@ts-ignore
    formData.append("isParent", isParent);
    if (parentCategory) {
      formData.append("parentCategory", parentCategory);
    }
    // Service-fee mode & service type only apply to Service/Both categories.
    if (isServiceType) {
      formData.append("serviceFeeMode", serviceFeeMode);
      formData.append("serviceType", serviceType);
    }

    try {
      setLoading(true);
      const response = await registerCategory(formData);
      if (response.status === 201) {
        // For a booking-mode service category, persist the per-tier fare
        // schedule against the freshly created category.
        if (isServiceType && serviceFeeMode === "booking") {
          const newCategoryId = String(
            response.data?.id ?? response.data?.result?.id ?? ""
          );
          if (newCategoryId) {
            const fees: BookingFees = {};
            for (const tier of BOOKING_TIERS) {
              fees[tier] = Number(bookingFares[tier]) || 0;
            }
            await upsertCategoryBookingFees(newCategoryId, fees);
          }
        }
        Alert.alert("success", "The category was created successfully.");
        setTitle("");
        setDescription("");
        setType("");
        setFeaturedImage(null);
        setParentCategory("");
        setIsFeatured(false);
        setIsParent(false);
        setServiceFeeMode("standard");
        setServiceType("contact");
        setBookingFares({});
        await fetchParentCategories();
        setLoading(false);
      }
    } catch (error: any) {
      console.error("Error Creating Category", error.response.data.message);
      Alert.alert("Error", "Error Creating Category");
    } finally {
      setLoading(false);
    }
  };

  // Picker items derived from the fetched parent categories (UI mapping only —
  // value remains item.id and label remains item.title, as before).
  const parentItems: PickerItem[] = parentCategories.map((item: any) => ({
    id: String(item.id),
    label: item.title,
  }));

  const selectedTypeLabel =
    TYPE_OPTIONS.find((o) => o.id === type)?.label ?? "";
  const selectedParentLabel =
    parentItems.find((o) => o.id === parentCategory)?.label ?? "";
  const selectedFeeModeLabel =
    FEE_MODE_OPTIONS.find((o) => o.id === serviceFeeMode)?.label ?? "";
  const selectedServiceTypeLabel =
    SERVICE_TYPE_OPTIONS.find((o) => o.id === serviceType)?.label ?? "";

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
        showsVerticalScrollIndicator={false}
      >
        <Input
          label="Title"
          value={title}
          onChangeText={(e) => setTitle(e)}
          placeholder="Category title"
        />

        <Input
          label="Enter Description"
          value={description}
          onChangeText={(e) => setDescription(e)}
          autoCapitalize="none"
          placeholder="Short description"
        />

        <SelectRow
          label="Enter Category Type"
          value={selectedTypeLabel}
          placeholder="Select Type"
          onPress={() => setTypeSheetOpen(true)}
        />

        {/* Service type — only for Service / Both categories */}
        {isServiceType && (
          <SelectRow
            label="Service type"
            value={selectedServiceTypeLabel}
            placeholder="Select service type"
            onPress={() => setServiceTypeSheetOpen(true)}
          />
        )}

        {/* Service fee mode — only for Service / Both categories */}
        {isServiceType && (
          <SelectRow
            label="Service fee mode"
            value={selectedFeeModeLabel}
            placeholder="Select fee mode"
            onPress={() => setFeeModeSheetOpen(true)}
          />
        )}

        {/* Booking fare editor — one USD input per area tier */}
        {showFareEditor && (
          <Card padding="lg" style={{ gap: spacing.md }}>
            <View style={{ gap: spacing.xs }}>
              <Text variant="bodyMed" weight="600">
                Booking fares (USD)
              </Text>
              <Text variant="caption" color="textMuted">
                Set the merged call-out + booking fee per area tier. Blank counts
                as 0.
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
          </Card>
        )}

        {/* Featured image upload — same expo-image-picker flow, restyled tile */}
        <View style={{ gap: spacing.sm }}>
          <Text variant="bodySm" weight="600" color="textSecondary">
            Upload Featured Image
          </Text>
          <PressableScale
            onPress={pickImageAsync}
            accessibilityRole="button"
            accessibilityLabel="Upload featured image"
            style={{
              alignItems: "center",
              justifyContent: "center",
              height: 100,
              borderRadius: radius.md,
              borderWidth: 1,
              borderStyle: "dashed",
              borderColor: colors.border,
              backgroundColor: colors.surfaceAlt,
            }}
          >
            <Icon name="upload" size={24} color={colors.textMuted} />
            <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
              Tap to choose an image
            </Text>
          </PressableScale>

          {featuredImage && (
            <View style={{ alignItems: "center" }}>
              <Image
                source={{ uri: featuredImage }}
                style={{
                  width: 100,
                  height: 100,
                  resizeMode: "contain",
                  borderRadius: radius.sm,
                }}
              />
            </View>
          )}
        </View>

        {/* Toggle rows replace the FontAwesome checkboxes — same boolean state */}
        <Card padding="sm" style={{ gap: spacing.sm }}>
          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              paddingVertical: spacing.xs,
            }}
          >
            <Text variant="body" style={{ flex: 1 }}>
              Select Parent Category
            </Text>
            <Toggle
              value={hasParent}
              onValueChange={() => setHasParent(!hasParent)}
              accessibilityLabel="Select parent category"
            />
          </View>

          {hasParent && (
            <SelectRow
              value={selectedParentLabel}
              placeholder="Select Parent Category"
              onPress={() => setParentSheetOpen(true)}
            />
          )}

          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              paddingVertical: spacing.xs,
            }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="body">Is Parent</Text>
              <Text variant="caption" color="textMuted">
                Parent Category?
              </Text>
            </View>
            <Toggle
              value={isParent}
              onValueChange={() => setIsParent(!isParent)}
              accessibilityLabel="Is parent"
            />
          </View>

          <View
            style={{
              flexDirection: "row",
              alignItems: "center",
              justifyContent: "space-between",
              paddingVertical: spacing.xs,
            }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="body">Is Featured</Text>
              <Text variant="caption" color="textMuted">
                Featured?
              </Text>
            </View>
            <Toggle
              value={isFeatured}
              onValueChange={() => setIsFeatured(!isFeatured)}
              accessibilityLabel="Is featured"
            />
          </View>
        </Card>

        <Button
          title="Continue"
          onPress={createCategory}
          disabled={loading}
          loading={loading}
          size="lg"
        />
      </ScrollView>

      {/* Category-type picker */}
      <PickerSheet
        visible={typeSheetOpen}
        onClose={() => setTypeSheetOpen(false)}
        title="Select Type"
        items={TYPE_OPTIONS}
        selectedId={type}
        searchable={false}
        onSelect={(item) => {
          setType(item.id);
          setTypeSheetOpen(false);
        }}
      />

      {/* Service-type picker */}
      <PickerSheet
        visible={serviceTypeSheetOpen}
        onClose={() => setServiceTypeSheetOpen(false)}
        title="Service type"
        items={SERVICE_TYPE_OPTIONS}
        selectedId={serviceType}
        searchable={false}
        onSelect={(item) => {
          setServiceType(item.id as ServiceType);
          setServiceTypeSheetOpen(false);
        }}
      />

      {/* Service-fee-mode picker */}
      <PickerSheet
        visible={feeModeSheetOpen}
        onClose={() => setFeeModeSheetOpen(false)}
        title="Service fee mode"
        items={FEE_MODE_OPTIONS}
        selectedId={serviceFeeMode}
        searchable={false}
        onSelect={(item) => {
          setServiceFeeMode(item.id as "standard" | "booking");
          setFeeModeSheetOpen(false);
        }}
      />

      {/* Parent-category picker */}
      <PickerSheet
        visible={parentSheetOpen}
        onClose={() => setParentSheetOpen(false)}
        title="Select Parent Category"
        items={parentItems}
        selectedId={parentCategory}
        emptyText="No parent categories"
        onSelect={(item) => {
          setParentCategory(item.id);
          setParentSheetOpen(false);
        }}
      />
    </SafeAreaView>
  );
};

export default AddCategoryScreen;
