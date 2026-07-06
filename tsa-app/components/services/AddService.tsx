// components/services/AddService.tsx
//
// Add / edit-service form. A merchant fills in service details, picks images,
// and POSTs a multipart product (type=Service) to the API. After the base
// product exists, the type-specific config (fees / menu / availability / policies)
// is persisted through the serviceListingApi.
//
// The form branches on the chosen category's `serviceType`:
//   - contact : portfolio images + basic info + Access/Call-Out fee + coverage
//   - order   : basic info + menu builder + delivery-fee tiers + availability + coverage
//   - booking : basic info + booking-fee tiers + access/call-out + availability +
//               slot config + policies + address/coverage/operating company
//
// TSA Signature design system only (@/theme + @/components/ui).
import {
  Image,
  ScrollView,
  View,
  Alert,
  Platform,
  KeyboardAvoidingView,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import React, { useState, useEffect, useRef } from "react";
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
import {
  getServiceProfile,
  upsertServiceProfile,
  listServiceMenu,
  createServiceMenuItem,
  updateServiceMenuItem,
  deleteServiceMenuItem,
  getServiceAvailability,
  replaceServiceAvailability,
  AvailabilityWindow,
  type BookingField,
} from "@/services/serviceListingApi";
import MenuBuilder, { MenuDraft, emptyMenuDraft } from "./MenuBuilder";
import AvailabilityEditor from "./AvailabilityEditor";
import BookingConfigBuilder from "./BookingConfigBuilder";
import PolicyFields, { PolicyValues, emptyPolicyValues } from "./PolicyFields";

type ServiceType = "contact" | "order" | "booking";

interface AddServiceProps {
  // When set, the form loads that product and its service config for editing.
  productId?: string;
}

const AddService: React.FC<AddServiceProps> = ({ productId: editProductId }) => {
  const { token } = useAuth();
  const { colors, spacing, radius } = useTheme();
  const isEdit = !!editProductId;

  const [loading, setLoading] = useState(false);
  const [prefetching, setPrefetching] = useState(false);
  const [productImages, setProductImages] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [productName, setProductName] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [email, setEmail] = useState("");
  const [location, setLocation] = useState<LocationValue>({ country: '', state: '', city: '' });
  const [category, setCategory] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [categories, setCategories] = useState<
    { id: string; title: string; serviceType?: ServiceType }[]
  >([]);
  const [categorySheetVisible, setCategorySheetVisible] = useState(false);

  // Standard fixed price (order/contact optional).
  const [price, setPrice] = useState("");

  // Per-zone distance fee (reuses the product shipping* columns). Used as the
  // call-out fee (contact) or delivery/booking fee tiers (order/booking).
  // "shippingResidential" is now a priceable tier for order/booking.
  const [baseCalloutFee, setBaseCalloutFee] = useState("");
  const [calloutResidential, setCalloutResidential] = useState("");
  const [calloutSameCity, setCalloutSameCity] = useState("");
  const [calloutSameState, setCalloutSameState] = useState("");
  const [calloutSameCountry, setCalloutSameCountry] = useState("");
  const [calloutInternational, setCalloutInternational] = useState("");

  // Service-profile fields (serviceListingApi).
  const [accessFeeUsd, setAccessFeeUsd] = useState("");
  const [calloutFeeUsd, setCalloutFeeUsd] = useState("");
  const [slotDurationMinutes, setSlotDurationMinutes] = useState("");
  const [maxCapacityPerSlot, setMaxCapacityPerSlot] = useState("");
  const [serviceAddress, setServiceAddress] = useState("");
  const [coverageArea, setCoverageArea] = useState("");
  const [operatingCompany, setOperatingCompany] = useState("");
  const [policies, setPolicies] = useState<PolicyValues>(emptyPolicyValues());
  // Booking-form field config (unified booking engine) — the enabled customer
  // form fields the marketplace auto-generates the booking form from.
  const [bookingFields, setBookingFields] = useState<BookingField[]>([]);

  // Menu (order) + availability (order/booking).
  const [menuItems, setMenuItems] = useState<MenuDraft[]>([emptyMenuDraft()]);
  const [availability, setAvailability] = useState<AvailabilityWindow[]>([]);

  // Ids of menu items loaded on edit-prefetch, used to reconcile deletes on save.
  const originalMenuIds = useRef<string[]>([]);

  const maxDescriptionLength = 500;

  // The chosen category's serviceType drives the whole form shape.
  const selectedCategory = categories.find((c) => c.id === category);
  const serviceType: ServiceType = selectedCategory?.serviceType ?? "contact";
  const isContact = serviceType === "contact";
  const isOrder = serviceType === "order";
  const isBooking = serviceType === "booking";

  // Whether the current type shows the per-zone distance-fee tiers, and whether
  // the residential tier is priceable (order/booking) vs free (contact).
  const showDistanceTiers = isContact || isOrder || isBooking;
  const residentialPriceable = isOrder || isBooking;

  // Typing a base fee auto-fills the zone rates below, each still editable.
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
                serviceType: c.serviceType,
              }))
            : []
        );
      } catch (error) {
        // Categories will show empty picker
      }
    };
    if (token) fetchCategories();
  }, [token]);

  // On edit, prefetch the product + service config and populate the form.
  useEffect(() => {
    const prefetch = async () => {
      if (!editProductId) return;
      setPrefetching(true);
      try {
        // Base product.
        const prodRes = await axios.get(`${baseUrl}/products/${editProductId}`, {
          headers: { Authorization: token },
        });
        const p = prodRes.data?.data ?? prodRes.data;
        if (p) {
          setProductName(p.name ?? "");
          setDescription(p.description ?? "");
          setEmail(p.email ?? "");
          setPhoneNumber(p.phoneNumber ?? "");
          setCompanyName(p.companyName ?? "");
          setCategory(typeof p.category === "string" ? p.category : p.category?.id ?? "");
          if (p.price) setPrice(String(p.price));
          if (p.shippingResidential != null) setCalloutResidential(String(p.shippingResidential));
          if (p.shippingSameCity != null) setCalloutSameCity(String(p.shippingSameCity));
          if (p.shippingSameState != null) setCalloutSameState(String(p.shippingSameState));
          if (p.shippingSameCountry != null) setCalloutSameCountry(String(p.shippingSameCountry));
          if (p.shippingInternational != null) setCalloutInternational(String(p.shippingInternational));
          if (Array.isArray(p.images)) {
            setProductImages(p.images.map((im: any) => im.url).filter(Boolean));
          }
        }

        // Service profile.
        const profRes = await getServiceProfile(editProductId);
        const prof = profRes.data;
        if (prof) {
          if (prof.accessFeeUsd) setAccessFeeUsd(String(prof.accessFeeUsd));
          if (prof.calloutFeeUsd) setCalloutFeeUsd(String(prof.calloutFeeUsd));
          if (prof.slotDurationMinutes) setSlotDurationMinutes(String(prof.slotDurationMinutes));
          if (prof.maxCapacityPerSlot) setMaxCapacityPerSlot(String(prof.maxCapacityPerSlot));
          setServiceAddress(prof.serviceAddress ?? "");
          setCoverageArea(prof.coverageArea ?? "");
          setOperatingCompany(prof.operatingCompany ?? "");
          setPolicies({
            cancellationPolicy: prof.cancellationPolicy ?? "",
            reschedulePolicy: prof.reschedulePolicy ?? "",
            refundPolicy: prof.refundPolicy ?? "",
            bookingRules: prof.bookingRules ?? "",
          });
          if (prof.fieldConfig?.fields?.length) setBookingFields(prof.fieldConfig.fields);
        }

        // Menu.
        const menuRes = await listServiceMenu(editProductId);
        const items = menuRes.data?.items;
        if (Array.isArray(items) && items.length) {
          originalMenuIds.current = items.map((it) => it.id).filter(Boolean);
          setMenuItems(
            items.map((it) => ({
              id: it.id,
              name: it.name,
              description: it.description ?? "",
              priceUsd: it.priceUsd != null ? String(it.priceUsd) : "",
              imageUrl: it.imageUrl,
            }))
          );
        }

        // Availability.
        const availRes = await getServiceAvailability(editProductId);
        if (Array.isArray(availRes.data?.windows)) {
          setAvailability(availRes.data!.windows);
        }
      } catch {
        // Best-effort prefetch — leave blanks the merchant can fill in.
      } finally {
        setPrefetching(false);
      }
    };
    if (token) prefetch();
  }, [editProductId, token]);

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

  // Persist the type-specific service config after the base product exists.
  const persistServiceConfig = async (productId: string) => {
    const num = (s: string) => {
      const n = parseFloat(s);
      return isNaN(n) ? 0 : n;
    };

    // Profile — send the fields relevant to the current type.
    const profile: Record<string, any> = {};
    if (isContact || isBooking) {
      profile.accessFeeUsd = num(accessFeeUsd);
      profile.calloutFeeUsd = num(calloutFeeUsd);
    }
    if (isBooking) {
      profile.slotDurationMinutes = num(slotDurationMinutes);
      profile.maxCapacityPerSlot = num(maxCapacityPerSlot);
      profile.cancellationPolicy = policies.cancellationPolicy;
      profile.reschedulePolicy = policies.reschedulePolicy;
      profile.refundPolicy = policies.refundPolicy;
      profile.bookingRules = policies.bookingRules;
      if (operatingCompany) profile.operatingCompany = operatingCompany;
      // FieldConfig for the auto-generated customer booking form. Null clears it
      // (→ legacy slot/fare flow); a non-empty set enables the schema-driven form.
      profile.fieldConfig = bookingFields.length ? { fields: bookingFields } : null;
    }
    if (serviceAddress) profile.serviceAddress = serviceAddress;
    if (coverageArea) profile.coverageArea = coverageArea;

    if (Object.keys(profile).length > 0) {
      await upsertServiceProfile(productId, profile);
    }

    // Menu items (order only). Reconcile against the ids loaded on edit-prefetch:
    // existing items are updated, new drafts created, removed items deleted.
    if (isOrder) {
      const valid = menuItems.filter((m) => m.name.trim());
      for (let i = 0; i < valid.length; i++) {
        const m = valid[i];
        const payload = {
          name: m.name.trim(),
          description: m.description,
          priceUsd: num(m.priceUsd),
          imageUrl: m.imageUrl,
          sortOrder: i,
          isAvailable: true,
        };
        if (m.id) {
          await updateServiceMenuItem(productId, m.id, payload);
        } else {
          await createServiceMenuItem(productId, payload);
        }
      }
      const keptIds = new Set(valid.map((m) => m.id).filter(Boolean));
      for (const id of originalMenuIds.current) {
        if (!keptIds.has(id)) {
          await deleteServiceMenuItem(productId, id);
        }
      }
    }

    // Availability windows (contact + order + booking).
    if (availability.length > 0) {
      await replaceServiceAvailability(productId, availability);
    }
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
      (!isEdit && productImages.length === 0)
    ) {
      Alert.alert("Missing Fields", "Please fill all required fields and upload at least one image.");
      return;
    }
    setLoading(true);
    const formData = new FormData();
    // On create we always send local URIs. On edit, only newly-picked local
    // URIs (file://) are appended; existing remote URLs are left as-is server-side.
    productImages.forEach((uri, index) => {
      if (isEdit && /^https?:/.test(uri)) return;
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

    // Price: order/contact may set a fixed price; booking has no fixed price.
    if (isBooking) {
      formData.append("price", "0");
    } else {
      const parsedPrice = parseFloat(price);
      formData.append(
        "price",
        !isNaN(parsedPrice) && parsedPrice > 0 ? parsedPrice.toString() : "0"
      );
    }

    // Distance-fee tiers reuse the product shipping* columns. Residential is a
    // real priceable tier for order/booking; for contact it stays free (unsent).
    if (showDistanceTiers) {
      if (residentialPriceable && calloutResidential) {
        formData.append("shippingResidential", calloutResidential);
      }
      if (calloutSameCity) formData.append("shippingSameCity", calloutSameCity);
      if (calloutSameState) formData.append("shippingSameState", calloutSameState);
      if (calloutSameCountry) formData.append("shippingSameCountry", calloutSameCountry);
      if (calloutInternational) formData.append("shippingInternational", calloutInternational);
    }

    formData.append("phoneNumber", phoneNumber);
    formData.append("category", category);
    formData.append("companyName", companyName);

    try {
      const authHeader = token?.startsWith('Bearer ') ? token : `Bearer ${token}`;
      const response = isEdit
        ? await axios.put(`${baseUrl}/products/${editProductId}`, formData, {
            headers: { Authorization: authHeader, "Content-Type": "multipart/form-data" },
          })
        : await axios.post(`${baseUrl}/products`, formData, {
            headers: { Authorization: authHeader, "Content-Type": "multipart/form-data" },
          });

      // Resolve the product id (create returns the new product; edit reuses it).
      const productId =
        editProductId ||
        response?.data?.data?.id ||
        response?.data?.data?._id ||
        response?.data?.id;

      if (productId) {
        try {
          await persistServiceConfig(productId);
        } catch {
          // The base product saved; surface config errors softly.
          Alert.alert(
            "Partly saved",
            "The service was saved, but some extra details could not be stored. You can edit the listing to retry."
          );
        }
      }

      setLoading(false);
      Alert.alert("Success", isEdit ? "Service updated successfully!" : "Service listed successfully!");
      if (!isEdit) resetForm();
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
    setCalloutResidential("");
    setCalloutSameCity("");
    setCalloutSameState("");
    setCalloutSameCountry("");
    setCalloutInternational("");
    setAccessFeeUsd("");
    setCalloutFeeUsd("");
    setSlotDurationMinutes("");
    setMaxCapacityPerSlot("");
    setServiceAddress("");
    setCoverageArea("");
    setOperatingCompany("");
    setPolicies(emptyPolicyValues());
    setMenuItems([emptyMenuDraft()]);
    setAvailability([]);
  };

  const selectedCategoryTitle = categories.find((c) => c.id === category)?.title;

  const sectionTitle = (title: string) => (
    <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
      {title}
    </Text>
  );

  // The per-zone distance-fee editor, relabelled per type.
  const renderDistanceTiers = (heading: string, sub: string) => (
    <>
      <Text variant="title3" weight="600" style={{ marginTop: spacing['2xl'], marginBottom: spacing.xs }}>
        {heading}
      </Text>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.md }}>
        {sub}
      </Text>

      <Card padding="lg" style={{ gap: spacing.lg }}>
        <View>
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
            Quick-select base fee
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
          label="Base fee ($)"
          placeholder="e.g. 2.00"
          keyboardType="decimal-pad"
          value={baseCalloutFee}
          onChangeText={applyBaseCalloutFee}
          helper="Auto-fills the zone rates below. Edit any zone to override."
        />

        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <View style={{ flex: 1 }}>
            {residentialPriceable ? (
              <Input
                label="Same Residential Area"
                placeholder="0.00"
                keyboardType="decimal-pad"
                value={calloutResidential}
                onChangeText={setCalloutResidential}
              />
            ) : (
              <Input
                label="Same Residential Area"
                value="Free"
                editable={false}
                inputStyle={{ color: colors.textMuted } as any}
              />
            )}
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
  );

  // Access + optional call-out flat fees (contact + booking).
  const renderAccessFees = () => (
    <>
      {sectionTitle(isContact ? "Pricing" : "Access & call-out fees")}
      <Card padding="lg" style={{ gap: spacing.lg }}>
        <Input
          label={isContact ? "Access Fee ($) *" : "Access Fee ($)"}
          placeholder="e.g. 5.00"
          keyboardType="decimal-pad"
          value={accessFeeUsd}
          onChangeText={setAccessFeeUsd}
          helper="Charged to unlock/contact or start a booking."
        />
        <Input
          label="Call-Out Fee ($) — optional"
          placeholder="Leave blank if none"
          keyboardType="decimal-pad"
          value={calloutFeeUsd}
          onChangeText={setCalloutFeeUsd}
        />
      </Card>
    </>
  );

  const renderCoverage = (withAddress: boolean) => (
    <>
      {sectionTitle("Coverage & area")}
      <Card padding="lg" style={{ gap: spacing.lg }}>
        <Input
          label="Coverage Area"
          placeholder="e.g. Lagos Mainland & Island"
          value={coverageArea}
          onChangeText={setCoverageArea}
        />
        {withAddress && (
          <Input
            label="Service Address"
            placeholder="Where the service is rendered / picked up"
            value={serviceAddress}
            onChangeText={setServiceAddress}
          />
        )}
      </Card>
    </>
  );

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
            {isEdit ? "Edit Service" : "Add Service"}
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Fill in the details to list your service
          </Text>

          {prefetching && (
            <View style={{ marginTop: spacing.lg }}>
              <Spinner label="Loading…" />
            </View>
          )}

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

          {/* Type-specific sections. Only shown once a category is chosen. */}
          {category ? (
            <>
              {/* CONTACT: access/call-out fee + free-residential call-out tiers */}
              {isContact && (
                <>
                  {renderAccessFees()}
                  {renderDistanceTiers(
                    "Call-out fee ($)",
                    "Mobilisation/transport by buyer area. All optional — blank = free."
                  )}

                  {sectionTitle("Available days & hours")}
                  <AvailabilityEditor windows={availability} onChange={setAvailability} />

                  {renderCoverage(true)}
                </>
              )}

              {/* ORDER: menu + delivery-fee tiers (residential priceable) + hours */}
              {isOrder && (
                <>
                  {sectionTitle("Menu")}
                  <MenuBuilder items={menuItems} onChange={setMenuItems} />

                  {renderDistanceTiers(
                    "Delivery fee ($)",
                    "Per-area delivery cost, including same residential area."
                  )}

                  {sectionTitle("Operating days & hours")}
                  <AvailabilityEditor windows={availability} onChange={setAvailability} />

                  {renderCoverage(true)}
                </>
              )}

              {/* BOOKING: fee tiers + access/call-out + availability + slots + policies */}
              {isBooking && (
                <>
                  {renderDistanceTiers(
                    "Booking fee ($)",
                    "Per-area fee, including same residential area."
                  )}

                  {renderAccessFees()}

                  {sectionTitle("Availability")}
                  <AvailabilityEditor windows={availability} onChange={setAvailability} />

                  {sectionTitle("Slot configuration")}
                  <Card padding="lg" style={{ gap: spacing.lg }}>
                    <View style={{ flexDirection: 'row', gap: spacing.md }}>
                      <View style={{ flex: 1 }}>
                        <Input
                          label="Slot duration (min)"
                          placeholder="e.g. 60"
                          keyboardType="number-pad"
                          value={slotDurationMinutes}
                          onChangeText={setSlotDurationMinutes}
                        />
                      </View>
                      <View style={{ flex: 1 }}>
                        <Input
                          label="Max per slot"
                          placeholder="e.g. 1"
                          keyboardType="number-pad"
                          value={maxCapacityPerSlot}
                          onChangeText={setMaxCapacityPerSlot}
                        />
                      </View>
                    </View>
                  </Card>

                  {sectionTitle("Policies")}
                  <PolicyFields
                    value={policies}
                    onChange={(patch) => setPolicies((prev) => ({ ...prev, ...patch }))}
                  />

                  {sectionTitle("Booking form")}
                  <BookingConfigBuilder value={bookingFields} onChange={setBookingFields} />

                  {renderCoverage(true)}

                  {sectionTitle("Operating company (optional)")}
                  <Card padding="lg">
                    <Input
                      label="Operating Company"
                      placeholder="e.g. airline / transport operator"
                      value={operatingCompany}
                      onChangeText={setOperatingCompany}
                    />
                  </Card>
                </>
              )}
            </>
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
            title={isEdit ? "Save Changes" : "Publish Service"}
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
