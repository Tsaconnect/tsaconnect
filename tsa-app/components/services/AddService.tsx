// components/services/AddService.tsx
//
// Add-service form. A merchant/user fills in service details, picks one or more
// local images, and POSTs a multipart product (type=Service) to the API.
//
// Migrated onto the TSA Signature design system (@/theme + @/components/ui).
// Visual/structural restyle ONLY — the data fetching (categories GET, multipart
// POST /products), state, image-picker logic (local URIs in FormData), field
// validation, Alert flows, and the LocationPicker integration are unchanged.
import {
  Image,
  ScrollView,
  View,
  Alert,
  Platform,
  KeyboardAvoidingView,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import React, { useState, useEffect } from "react";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import LocationPicker, { LocationValue } from "../common/LocationPicker";
import { calculateZoneRates, PACKAGE_SIZE_PRESETS } from "@/constants/shipping";
import { useTheme } from "@/theme";
import {
  Button,
  Card,
  Icon,
  Input,
  PickerSheet,
  PressableScale,
  SelectRow,
  Spinner,
  Text,
} from "@/components/ui";

const AddService = () => {
  const { token } = useAuth();
  const { colors, spacing, radius } = useTheme();
  const [loading, setLoading] = useState(false);
  const [productImages, setProductImages] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [productName, setProductName] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [email, setEmail] = useState("");
  const [location, setLocation] = useState<LocationValue>({ country: '', state: '', city: '' });
  const [category, setCategory] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [categories, setCategories] = useState<
    { id: string; title: string; serviceFeeMode?: "standard" | "booking" }[]
  >([]);
  const [categorySheetVisible, setCategorySheetVisible] = useState(false);

  // Pricing (standard call-out fee model). All optional — blank = free.
  const [price, setPrice] = useState("");
  const [baseCalloutFee, setBaseCalloutFee] = useState("");
  const [calloutSameCity, setCalloutSameCity] = useState("");
  const [calloutSameState, setCalloutSameState] = useState("");
  const [calloutSameCountry, setCalloutSameCountry] = useState("");
  const [calloutInternational, setCalloutInternational] = useState("");

  const maxDescriptionLength = 500;

  // The chosen category's fee mode drives the pricing section. Booking-mode
  // categories hide all pricing inputs (fares are admin-set per category).
  const selectedCategory = categories.find((c) => c.id === category);
  const isBooking = selectedCategory?.serviceFeeMode === "booking";

  // Mirrors the product add form's base-fee auto-fill: typing a base fee fills
  // the zone rates below, each still individually editable.
  const applyBaseCalloutFee = (baseStr: string) => {
    setBaseCalloutFee(baseStr);
    const base = parseFloat(baseStr);
    if (isNaN(base) || base < 0) return;
    const rates = calculateZoneRates(base);
    setCalloutSameCity(rates.sameCity.toFixed(2));
    setCalloutSameState(rates.sameState.toFixed(2));
    setCalloutSameCountry(rates.sameCountry.toFixed(2));
    setCalloutInternational(rates.international.toFixed(2));
  };

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(
          `${baseUrl}/products/category/all?type=Service&active=true`,
          { headers: { Authorization: token } }
        );
        const data = response.data.data;
        setCategories(
          Array.isArray(data)
            ? data.map((c: any) => ({
                id: c.id ?? c._id,
                title: c.title,
                serviceFeeMode: c.serviceFeeMode,
              }))
            : []
        );
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

  const createService = async () => {
    if (
      !productName ||
      !location.country ||
      !location.state ||
      !phoneNumber ||
      !email ||
      !category ||
      !description ||
      productImages.length === 0
    ) {
      Alert.alert("Missing Fields", "Please fill all required fields and upload at least one image.");
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
    formData.append("location", [location.city, location.state, location.country].filter(Boolean).join(', '));
    formData.append("type", "Service");
    formData.append("email", email);
    if (isBooking) {
      // Booking categories: fares are admin-set per category; send no price/call-out.
      formData.append("price", "0");
    } else {
      // Standard: send the entered fixed price (0 if quoted per job) and the
      // per-zone call-out amounts reusing the product shipping* columns.
      // "Same residential area" is the implicit free tier — no field sent.
      const parsedPrice = parseFloat(price);
      formData.append(
        "price",
        !isNaN(parsedPrice) && parsedPrice > 0 ? parsedPrice.toString() : "0"
      );
      if (calloutSameCity) formData.append("shippingSameCity", calloutSameCity);
      if (calloutSameState) formData.append("shippingSameState", calloutSameState);
      if (calloutSameCountry) formData.append("shippingSameCountry", calloutSameCountry);
      if (calloutInternational) formData.append("shippingInternational", calloutInternational);
    }
    formData.append("phoneNumber", phoneNumber);
    formData.append("category", category);
    formData.append("companyName", companyName);

    try {
      const response = await axios.post(`${baseUrl}/products`, formData, {
        headers: {
          Authorization: token?.startsWith('Bearer ') ? token : `Bearer ${token}`,
          "Content-Type": "multipart/form-data",
        },
      });
      if (response) {
        setLoading(false);
        Alert.alert("Success", "Service listed successfully!");
        resetForm();
      }
    } catch (error: any) {
      setLoading(false);
      const message = error?.response?.data?.message || "Failed to create service.";
      Alert.alert("Error", message);
    }
  };

  const resetForm = () => {
    setPhoneNumber("");
    setCategory("");
    setDescription("");
    setEmail("");
    setLocation({ country: '', state: '', city: '' });
    setProductImages([]);
    setProductName("");
    setCompanyName("");
    setPrice("");
    setBaseCalloutFee("");
    setCalloutSameCity("");
    setCalloutSameState("");
    setCalloutSameCountry("");
    setCalloutInternational("");
  };

  const selectedCategoryTitle = categories.find((c) => c.id === category)?.title;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : undefined}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* Header */}
          <Text variant="title1" weight="700">
            Add Service
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Fill in the details to list your service
          </Text>

          {/* Image Upload */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Photos
          </Text>
          <PressableScale onPress={pickProductImages}>
            <View
              style={{
                backgroundColor: colors.surface,
                borderWidth: 2,
                borderColor: colors.accentMuted,
                borderStyle: 'dashed',
                borderRadius: radius.lg,
                paddingVertical: spacing['3xl'],
                alignItems: 'center',
                justifyContent: 'center',
              }}
            >
              <Icon name="camera" size={32} color={colors.accent} />
              <Text variant="bodyMed" weight="600" color="accent" style={{ marginTop: spacing.sm }}>
                Tap to upload images
              </Text>
              <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
                Showcase your service with quality photos
              </Text>
            </View>
          </PressableScale>

          {productImages.length > 0 && (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              contentContainerStyle={{ gap: spacing.md }}
              style={{ marginTop: spacing.md, marginBottom: spacing.xs }}
            >
              {productImages.map((uri, index) => (
                <View key={index} style={{ position: 'relative' }}>
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
                      position: 'absolute',
                      top: -6,
                      insetInlineEnd: -6,
                      width: 24,
                      height: 24,
                      borderRadius: radius.pill,
                      alignItems: 'center',
                      justifyContent: 'center',
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

          {/* Service Details */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Service Details
          </Text>

          <Card padding="lg" style={{ gap: spacing.lg }}>
            <Input
              label="Service Name *"
              value={productName}
              onChangeText={setProductName}
              placeholder="e.g. Plumbing & Pipe Repair"
            />

            <SelectRow
              label="Category *"
              value={selectedCategoryTitle}
              placeholder="Select a category"
              onPress={() => setCategorySheetVisible(true)}
            />

            <Input
              label="Description *"
              placeholder="Describe your service, expertise, and what clients can expect..."
              numberOfLines={5}
              multiline
              maxLength={maxDescriptionLength}
              counter
              value={description}
              onChangeText={setDescription}
              textAlignVertical="top"
              inputStyle={{ minHeight: 120 }}
            />
          </Card>

          {/* Pricing — reacts to the selected category's serviceFeeMode */}
          {category ? (
            isBooking ? (
              <>
                <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
                  Pricing
                </Text>
                <Card padding="lg">
                  <View style={{ flexDirection: 'row', gap: spacing.md }}>
                    <Icon name="info" size={20} color={colors.accent} />
                    <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                      Fares for this category are set by the admin and shown to the buyer at checkout.
                    </Text>
                  </View>
                </Card>
              </>
            ) : (
              <>
                <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
                  Pricing
                </Text>
                <Card padding="lg" style={{ gap: spacing.lg }}>
                  <Input
                    label="Service price ($)"
                    placeholder="Leave blank if quoted per job"
                    keyboardType="decimal-pad"
                    value={price}
                    onChangeText={setPrice}
                  />
                </Card>

                {/* Call-out fee — relabels the product "Shipping Rates" UI */}
                <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.xs }}>
                  Call-out fee ($)
                </Text>
                <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
                  Mobilisation/transport, by buyer area. All optional — blank = free.
                </Text>

                <Card padding="lg" style={{ gap: spacing.lg }}>
                  <View>
                    <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
                      Package size (optional quick-select)
                    </Text>
                    <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
                      {PACKAGE_SIZE_PRESETS.map((preset) => (
                        <PressableScale
                          key={preset.key}
                          onPress={() => applyBaseCalloutFee(String(preset.baseFee))}
                          style={{
                            flex: 1,
                            minWidth: 90,
                            paddingVertical: spacing.sm,
                            paddingHorizontal: spacing.sm,
                            borderRadius: radius.md,
                            borderWidth: 1,
                            borderColor: colors.border,
                            backgroundColor: colors.surface,
                            alignItems: 'center',
                            gap: 2,
                          }}
                        >
                          <Text variant="caption" weight="700" align="center">
                            {preset.label}
                          </Text>
                          <Text variant="caption" weight="600" color="accent">
                            ${preset.baseFee}
                          </Text>
                        </PressableScale>
                      ))}
                    </View>
                  </View>

                  <Input
                    label="Base call-out fee ($)"
                    placeholder="e.g. 2.00"
                    keyboardType="decimal-pad"
                    value={baseCalloutFee}
                    onChangeText={applyBaseCalloutFee}
                    helper="Auto-fills the zone rates below. Edit any zone to override."
                  />

                  <View style={{ flexDirection: 'row', gap: spacing.md }}>
                    <View style={{ flex: 1 }}>
                      <Input
                        label="Same Residential Area"
                        value="Free"
                        editable={false}
                        inputStyle={{ color: colors.textMuted } as any}
                      />
                    </View>
                    <View style={{ flex: 1 }}>
                      <Input
                        label="Same City"
                        placeholder="0.00"
                        keyboardType="decimal-pad"
                        value={calloutSameCity}
                        onChangeText={setCalloutSameCity}
                      />
                    </View>
                  </View>
                  <View style={{ flexDirection: 'row', gap: spacing.md }}>
                    <View style={{ flex: 1 }}>
                      <Input
                        label="Same State"
                        placeholder="0.00"
                        keyboardType="decimal-pad"
                        value={calloutSameState}
                        onChangeText={setCalloutSameState}
                      />
                    </View>
                    <View style={{ flex: 1 }}>
                      <Input
                        label="Same Country"
                        placeholder="0.00"
                        keyboardType="decimal-pad"
                        value={calloutSameCountry}
                        onChangeText={setCalloutSameCountry}
                      />
                    </View>
                  </View>
                  <View style={{ flexDirection: 'row', gap: spacing.md }}>
                    <View style={{ flex: 1 }}>
                      <Input
                        label="International"
                        placeholder="0.00"
                        keyboardType="decimal-pad"
                        value={calloutInternational}
                        onChangeText={setCalloutInternational}
                      />
                    </View>
                    <View style={{ flex: 1 }} />
                  </View>
                </Card>
              </>
            )
          ) : null}

          {/* Contact & Location */}
          <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Contact & Location
          </Text>

          <Card padding="lg" style={{ gap: spacing.lg }}>
            <Input
              label="Company Name"
              value={companyName}
              onChangeText={setCompanyName}
              placeholder="Your business name"
            />

            <LocationPicker
              value={location}
              onChange={setLocation}
              required={["country", "state"]}
            />

            <Input
              label="Phone Number *"
              value={phoneNumber}
              onChangeText={setPhoneNumber}
              keyboardType="phone-pad"
              placeholder="+234 800 000 0000"
              leftAdornment={<Icon name="mobile" size={20} color={colors.textSecondary} />}
            />

            <Input
              label="Email *"
              value={email}
              onChangeText={setEmail}
              keyboardType="email-address"
              autoCapitalize="none"
              placeholder="you@example.com"
              leftAdornment={<Icon name="mail" size={20} color={colors.textSecondary} />}
            />
          </Card>

          {/* Submit */}
          {loading && (
            <View style={{ marginTop: spacing.lg }}>
              <Spinner label="Submitting…" />
            </View>
          )}

          <Button
            title="Publish Service"
            leftIcon="upload"
            onPress={createService}
            disabled={loading}
            style={{ marginTop: spacing['2xl'] }}
          />
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={categorySheetVisible}
        onClose={() => setCategorySheetVisible(false)}
        title="Select a category"
        items={categories.map((item) => ({ id: item.id, label: item.title }))}
        selectedId={category || undefined}
        onSelect={(item) => {
          setCategory(item.id);
          setCategorySheetVisible(false);
        }}
        searchable
        emptyText="No categories available"
      />
    </SafeAreaView>
  );
};

export default AddService;
