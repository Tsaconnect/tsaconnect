// app/merchants/inventory/add/index.tsx
//
// Add inventory product form (merchant). Restyled onto the TSA Signature design
// system (@/theme + @/components/ui). All logic is preserved verbatim: the
// getCategories / createProduct API calls, the expo-image-picker gallery/camera
// flow that appends LOCAL asset URIs and builds the multipart FormData (images
// are NOT pre-uploaded — they ride along on api.createProduct), the zone-rate
// auto-fill math (calculateZoneRates / PACKAGE_SIZE_PRESETS), attribute
// add/remove, the full validateForm() rule set, LocationPicker wiring, and the
// reset + router.back() success flow. Only the chrome changed: legacy GOLD/
// StyleSheet → tokens + primitives; the result Modal → useToast; the category
// Modal → a Sheet keeping the rich CategoryAvatar list.
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  TextInput,
  ScrollView,
  Image,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import * as ImagePicker from 'expo-image-picker';
import { useAuth } from '@/AuthContext/AuthContext';
import api from '@/components/services/api';
import LocationPicker from '../../../../components/common/LocationPicker';
import { calculateZoneRates, PACKAGE_SIZE_PRESETS } from '@/constants/shipping';
import { useTheme } from '@/theme';
import {
  Avatar,
  Button,
  Icon,
  Input,
  PressableScale,
  SelectRow,
  Sheet,
  Spinner,
  Text,
  useToast,
} from '@/components/ui';

const CategoryAvatar = ({ category }: { category: any }) => {
  const { radius } = useTheme();
  if (category.icon) {
    return (
      <Image
        source={{ uri: category.icon }}
        style={{ width: 40, height: 40, borderRadius: radius.pill }}
      />
    );
  }
  const initial = (category.title || '?')[0].toUpperCase();
  return <Avatar name={initial} size={40} />;
};

const AddProduct = () => {
  const { colors, spacing, radius } = useTheme();
  const toast = useToast();
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);

  const [productName, setProductName] = useState('');
  const [description, setDescription] = useState('');
  const [price, setPrice] = useState('');
  const [stock, setStock] = useState('0');
  const [categoryId, setCategoryId] = useState('');
  const [categoryName, setCategoryName] = useState('');
  const [location, setLocation] = useState({ country: "", state: "", city: "" });
  const [phoneNumber, setPhoneNumber] = useState('');
  const [email, setEmail] = useState('');
  const [companyName, setCompanyName] = useState('');
  const [baseShippingFee, setBaseShippingFee] = useState('');
  const [shippingSameCity, setShippingSameCity] = useState('');
  const [shippingSameState, setShippingSameState] = useState('');
  const [shippingSameCountry, setShippingSameCountry] = useState('');
  const [shippingInternational, setShippingInternational] = useState('');

  const applyBaseShippingFee = (baseStr: string) => {
    setBaseShippingFee(baseStr);
    const base = parseFloat(baseStr);
    if (isNaN(base) || base < 0) return;
    const rates = calculateZoneRates(base);
    setShippingSameCity(rates.sameCity.toFixed(2));
    setShippingSameState(rates.sameState.toFixed(2));
    setShippingSameCountry(rates.sameCountry.toFixed(2));
    setShippingInternational(rates.international.toFixed(2));
  };

  const applyPackagePreset = (baseFee: number) => {
    applyBaseShippingFee(String(baseFee));
  };
  const [productImages, setProductImages] = useState<string[]>([]);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [categoriesLoading, setCategoriesLoading] = useState(true);
  const [categoryModalVisible, setCategoryModalVisible] = useState(false);
  const [categorySearch, setCategorySearch] = useState('');

  const [errors, setErrors] = useState<Record<string, string>>({});

  const maxDescriptionLength = 500;

  const descriptionRef = useRef<TextInput>(null);
  const priceRef = useRef<TextInput>(null);
  const stockRef = useRef<TextInput>(null);
  const phoneRef = useRef<TextInput>(null);
  const emailRef = useRef<TextInput>(null);
  const companyRef = useRef<TextInput>(null);

  useEffect(() => {
    fetchCategories();
  }, []);

  const fetchCategories = async () => {
    setCategoriesLoading(true);
    try {
      const response = await api.getCategories({ type: 'Product', active: true });
      if (response.success && response.data) {
        setCategories(response.data);
        if (response.data.length === 1) {
          const cat = response.data[0];
          const catId = cat.id || cat._id;
          if (catId && cat.title) {
            setCategoryId(catId);
            setCategoryName(cat.title);
          }
        }
      }
    } catch (error) {
      showModal('error', 'Failed to load categories');
    } finally {
      setCategoriesLoading(false);
    }
  };

  const pickProductImages = async () => {
    try {
      const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== 'granted') {
        showModal('error', 'Permission to access media library denied!');
        return;
      }

      const result = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        allowsMultipleSelection: true,
        aspect: [4, 3],
        quality: 0.8,
        selectionLimit: 10 - productImages.length,
      });

      if (!result.canceled && result.assets.length > 0) {
        const newImages = result.assets.map(asset => asset.uri);
        setProductImages(prev => [...prev, ...newImages].slice(0, 10));
      }
    } catch (error) {
      showModal('error', 'Failed to pick images');
    }
  };

  const removeImage = (index: number) => {
    setProductImages(prev => prev.filter((_, i) => i !== index));
  };

  const takePhoto = async () => {
    try {
      const { status } = await ImagePicker.requestCameraPermissionsAsync();
      if (status !== 'granted') {
        showModal('error', 'Camera permission denied!');
        return;
      }

      const result = await ImagePicker.launchCameraAsync({
        allowsEditing: true,
        aspect: [4, 3],
        quality: 0.8,
      });

      if (!result.canceled && result.assets[0]) {
        setProductImages(prev => [...prev, result.assets[0].uri].slice(0, 10));
      }
    } catch (error) {
      showModal('error', 'Failed to take photo');
    }
  };

  const handleAddAttribute = () => {
    setAttributes([...attributes, { name: '', value: '' }]);
  };

  const handleRemoveAttribute = (index: number) => {
    const newAttributes = [...attributes];
    newAttributes.splice(index, 1);
    setAttributes(newAttributes);
  };

  const handleAttributeChange = (index: number, field: 'name' | 'value', value: string) => {
    const newAttributes = [...attributes];
    newAttributes[index][field] = value;
    setAttributes(newAttributes);
  };

  const validateForm = () => {
    const newErrors: Record<string, string> = {};

    if (!productName.trim()) newErrors.productName = 'Product name is required';
    if (!description.trim()) newErrors.description = 'Description is required';
    if (description.length > maxDescriptionLength) newErrors.description = `Description must be less than ${maxDescriptionLength} characters`;
    if (!price.trim()) newErrors.price = 'Price is required';
    if (isNaN(parseFloat(price)) || parseFloat(price) <= 0) newErrors.price = 'Price must be a positive number';
    if (!stock.trim()) newErrors.stock = 'Stock is required';
    if (isNaN(parseInt(stock)) || parseInt(stock) < 0) newErrors.stock = 'Stock must be a non-negative number';
    if (!categoryId) newErrors.category = 'Category is required';
    if (!location.country) newErrors.location = 'Country is required';
    else if (!location.state) newErrors.location = 'State is required';
    if (!phoneNumber.trim()) newErrors.phoneNumber = 'Phone number is required';
    if (!email.trim()) newErrors.email = 'Email is required';
    if (email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) newErrors.email = 'Invalid email address';
    if (productImages.length === 0) newErrors.images = 'At least one image is required';

    attributes.forEach((attr, index) => {
      if (!attr.name.trim()) newErrors[`attr_name_${index}`] = 'Attribute name is required';
      if (!attr.value.trim()) newErrors[`attr_value_${index}`] = 'Attribute value is required';
    });

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const createProduct = async () => {
    if (!validateForm()) {
      showModal('error', 'Please fix all errors before submitting');
      return;
    }

    setLoading(true);

    try {
      const formData = new FormData();

      productImages.forEach((uri, index) => {
        const filename = uri.split('/').pop() || `image_${index}.jpg`;
        const match = /\.(\w+)$/.exec(filename);
        const type = match ? `image/${match[1]}` : 'image/jpeg';
        //@ts-ignore
        formData.append('images', {
          uri,
          name: filename,
          type,
        });
      });

      formData.append('name', productName);
      formData.append('description', description);
      formData.append('price', parseFloat(price).toString());
      formData.append('stock', parseInt(stock).toString());
      formData.append('category', categoryId);
      formData.append('location', [location.city, location.state, location.country].filter(Boolean).join(', '));
      formData.append('phoneNumber', phoneNumber);
      formData.append('email', email);
      if (companyName.trim()) formData.append('companyName', companyName);
      if (shippingSameCity) formData.append('shippingSameCity', shippingSameCity);
      if (shippingSameState) formData.append('shippingSameState', shippingSameState);
      if (shippingSameCountry) formData.append('shippingSameCountry', shippingSameCountry);
      if (shippingInternational) formData.append('shippingInternational', shippingInternational);
      if (attributes.length > 0) {
        formData.append('attributes', JSON.stringify(attributes));
      }

      const response = await api.createProduct(formData);

      if (response.success) {
        showModal('success', 'Product created successfully!');
        resetForm();
        setTimeout(() => {
          if (router.canGoBack()) {
            router.back();
          }
        }, 2000);
      } else {
        throw new Error(response.message || 'Failed to create product');
      }
    } catch (error) {
      console.error('Create product error:', error);
      showModal('error', (error as any).message || 'An error occurred while creating product');
    } finally {
      setLoading(false);
    }
  };

  const resetForm = () => {
    setProductName('');
    setDescription('');
    setPrice('');
    setStock('0');
    setCategoryId('');
    setCategoryName('');
    setLocation({ country: "", state: "", city: "" });
    setPhoneNumber('');
    setEmail('');
    setCompanyName('');
    setProductImages([]);
    setAttributes([]);
    setErrors({});
  };

  // Result feedback now uses the toast system; signature is preserved so every
  // call site (validation, permission denials, success, server error) is verbatim.
  const showModal = (type: 'success' | 'error' | 'info', message: string) => {
    toast.show(message, type);
  };

  const filteredCategories = categories.filter(
    item => !categorySearch || item.title.toLowerCase().includes(categorySearch.toLowerCase())
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* Header */}
          <Text variant="title1" weight="700">
            Add Product
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
            Fill in the details to list your product
          </Text>

          {/* Image Upload */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              marginTop: spacing['2xl'],
              marginBottom: spacing.md,
            }}
          >
            <Text variant="title3" weight="700">
              Photos
            </Text>
            <Text variant="caption" color="textMuted">
              {productImages.length}/10
            </Text>
          </View>
          {errors.images && (
            <Text variant="caption" color="danger" style={{ marginBottom: spacing.sm }}>
              {errors.images}
            </Text>
          )}

          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <PressableScale
              onPress={pickProductImages}
              style={{
                flex: 1,
                backgroundColor: colors.surface,
                borderWidth: 1.5,
                borderColor: colors.accent,
                borderStyle: 'dashed',
                borderRadius: radius.lg,
                paddingVertical: spacing['2xl'],
                alignItems: 'center',
                justifyContent: 'center',
                gap: spacing.xs,
              }}
            >
              <Icon name="image" size={26} color={colors.accent} />
              <Text variant="bodySm" weight="600" color="accent">
                Gallery
              </Text>
            </PressableScale>
            <PressableScale
              onPress={takePhoto}
              style={{
                flex: 1,
                backgroundColor: colors.surface,
                borderWidth: 1.5,
                borderColor: colors.accent,
                borderStyle: 'dashed',
                borderRadius: radius.lg,
                paddingVertical: spacing['2xl'],
                alignItems: 'center',
                justifyContent: 'center',
                gap: spacing.xs,
              }}
            >
              <Icon name="camera" size={26} color={colors.accent} />
              <Text variant="bodySm" weight="600" color="accent">
                Camera
              </Text>
            </PressableScale>
          </View>

          {productImages.length > 0 && (
            <ScrollView
              horizontal
              showsHorizontalScrollIndicator={false}
              style={{ marginTop: spacing.md }}
              contentContainerStyle={{ gap: spacing.sm }}
            >
              {productImages.map((uri, index) => (
                <View key={index} style={{ width: 80, height: 80 }}>
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
                    hitSlop={6}
                    accessibilityRole="button"
                    accessibilityLabel="Remove image"
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

          {/* Product Details */}
          <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Product Details
          </Text>

          <Input
            label="Product Name *"
            value={productName}
            onChangeText={setProductName}
            placeholder="e.g. Organic Honey Jar"
            returnKeyType="next"
            onSubmitEditing={() => descriptionRef.current?.focus()}
            error={errors.productName}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <View style={{ marginBottom: spacing.lg }}>
            {categoriesLoading ? (
              <View>
                <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                  Category *
                </Text>
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, paddingVertical: spacing.md }}>
                  <Spinner size="small" inline />
                  <Text variant="bodySm" color="textMuted">
                    Loading categories...
                  </Text>
                </View>
              </View>
            ) : categories.length === 0 ? (
              <View>
                <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                  Category *
                </Text>
                <PressableScale onPress={fetchCategories} style={{ paddingVertical: spacing.md }}>
                  <Text variant="bodySm" color="textMuted">
                    No categories found. Tap to retry.
                  </Text>
                </PressableScale>
              </View>
            ) : (
              <SelectRow
                label="Category *"
                value={categoryName || undefined}
                placeholder="Select a category"
                error={errors.category}
                onPress={() => {
                  setCategorySearch('');
                  setCategoryModalVisible(true);
                }}
              />
            )}
          </View>

          <View style={{ marginBottom: spacing.lg }}>
            <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
              Description *
            </Text>
            <View
              style={{
                borderRadius: radius.md,
                borderWidth: 1.5,
                borderColor: errors.description ? colors.danger : colors.border,
                backgroundColor: colors.surface,
                paddingHorizontal: spacing.md,
                paddingVertical: spacing.sm,
              }}
            >
              <TextInput
                ref={descriptionRef}
                placeholder="Describe your product, quality, and what buyers can expect..."
                placeholderTextColor={colors.textMuted}
                numberOfLines={5}
                multiline
                maxLength={maxDescriptionLength}
                value={description}
                onChangeText={setDescription}
                textAlignVertical="top"
                style={{ minHeight: 110, color: colors.textPrimary, fontSize: 16 }}
              />
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.xs }}>
              <Text variant="caption" color={errors.description ? 'danger' : 'textMuted'} style={{ flex: 1 }}>
                {errors.description || ''}
              </Text>
              <Text variant="caption" color="textMuted">
                {description.length}/{maxDescriptionLength}
              </Text>
            </View>
          </View>

          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <Input
                label="Price ($) *"
                placeholder="0.00"
                keyboardType="decimal-pad"
                value={price}
                onChangeText={setPrice}
                error={errors.price}
                helper={!errors.price ? '2% platform fee applies per sale' : undefined}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Input
                label="Stock *"
                placeholder="0"
                keyboardType="number-pad"
                value={stock}
                onChangeText={setStock}
                error={errors.stock}
              />
            </View>
          </View>

          {/* Shipping Rates */}
          <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Shipping Rates ($)
          </Text>

          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
            Package size (optional quick-select)
          </Text>
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, marginBottom: spacing.lg }}>
            {PACKAGE_SIZE_PRESETS.map((preset) => (
              <PressableScale
                key={preset.key}
                onPress={() => applyPackagePreset(preset.baseFee)}
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

          <Input
            label="Base shipping fee ($)"
            placeholder="e.g. 2.00"
            keyboardType="decimal-pad"
            value={baseShippingFee}
            onChangeText={applyBaseShippingFee}
            helper="Auto-fills the zone rates below. Edit any zone to override."
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <View style={{ flexDirection: 'row', gap: spacing.md, marginBottom: spacing.lg }}>
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
                value={shippingSameCity}
                onChangeText={setShippingSameCity}
              />
            </View>
          </View>
          <View style={{ flexDirection: 'row', gap: spacing.md, marginBottom: spacing.lg }}>
            <View style={{ flex: 1 }}>
              <Input
                label="Same State"
                placeholder="0.00"
                keyboardType="decimal-pad"
                value={shippingSameState}
                onChangeText={setShippingSameState}
              />
            </View>
            <View style={{ flex: 1 }}>
              <Input
                label="Same Country"
                placeholder="0.00"
                keyboardType="decimal-pad"
                value={shippingSameCountry}
                onChangeText={setShippingSameCountry}
              />
            </View>
          </View>
          <View style={{ flexDirection: 'row', gap: spacing.md, marginBottom: spacing.lg }}>
            <View style={{ flex: 1 }}>
              <Input
                label="International"
                placeholder="0.00"
                keyboardType="decimal-pad"
                value={shippingInternational}
                onChangeText={setShippingInternational}
              />
            </View>
            <View style={{ flex: 1 }} />
          </View>

          {/* Contact & Location */}
          <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Contact & Location
          </Text>

          <Input
            label="Company Name"
            value={companyName}
            onChangeText={setCompanyName}
            placeholder="Your business name"
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <View style={{ marginBottom: spacing.lg }}>
            <LocationPicker
              value={location}
              onChange={setLocation}
              required={["country", "state"]}
            />
            {errors.location && (
              <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                {errors.location}
              </Text>
            )}
          </View>

          <Input
            label="Phone Number *"
            value={phoneNumber}
            onChangeText={setPhoneNumber}
            keyboardType="phone-pad"
            placeholder="+234 800 000 0000"
            returnKeyType="next"
            onSubmitEditing={() => emailRef.current?.focus()}
            leftAdornment={<Icon name="mobile" size={20} color={colors.accent} />}
            error={errors.phoneNumber}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          <Input
            label="Email *"
            value={email}
            onChangeText={setEmail}
            keyboardType="email-address"
            autoCapitalize="none"
            placeholder="you@example.com"
            leftAdornment={<Icon name="send" size={20} color={colors.accent} />}
            error={errors.email}
            containerStyle={{ marginBottom: spacing.lg }}
          />

          {/* Attributes */}
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              marginTop: spacing.sm,
              marginBottom: spacing.md,
            }}
          >
            <Text variant="title3" weight="700">
              Attributes
            </Text>
            <Button title="Add" leftIcon="plus" variant="secondary" size="sm" fullWidth={false} onPress={handleAddAttribute} />
          </View>

          {attributes.length > 0 ? (
            attributes.map((item, index) => (
              <View
                key={index}
                style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm, marginBottom: spacing.sm }}
              >
                <View style={{ flex: 1 }}>
                  <Input
                    placeholder="e.g. Size"
                    value={item.name}
                    onChangeText={(value) => handleAttributeChange(index, 'name', value)}
                    error={errors[`attr_name_${index}`]}
                  />
                </View>
                <View style={{ flex: 1 }}>
                  <Input
                    placeholder="e.g. Large"
                    value={item.value}
                    onChangeText={(value) => handleAttributeChange(index, 'value', value)}
                    error={errors[`attr_value_${index}`]}
                  />
                </View>
                <PressableScale
                  onPress={() => handleRemoveAttribute(index)}
                  hitSlop={6}
                  accessibilityRole="button"
                  accessibilityLabel="Remove attribute"
                  style={{ height: 48, width: 32, alignItems: 'center', justifyContent: 'center' }}
                >
                  <Icon name="trash" size={20} color={colors.danger} />
                </PressableScale>
              </View>
            ))
          ) : (
            <Text variant="bodySm" color="textMuted" align="center" style={{ paddingVertical: spacing.sm }}>
              Add attributes like size, color, material, etc.
            </Text>
          )}

          {/* Submit */}
          {loading && (
            <View style={{ marginTop: spacing.lg }}>
              <Spinner label="Submitting…" />
            </View>
          )}

          <Button
            title="Publish Product"
            leftIcon="cart"
            onPress={createProduct}
            disabled={loading}
            loading={loading}
            size="lg"
            style={{ marginTop: spacing['2xl'] }}
          />

          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.md }}>
            Fields marked with * are required
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>

      {/* Category Selection Sheet */}
      <Sheet
        visible={categoryModalVisible}
        onClose={() => setCategoryModalVisible(false)}
        title="Select Category"
      >
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
          <Input
            placeholder="Search categories..."
            value={categorySearch}
            onChangeText={setCategorySearch}
            autoCapitalize="none"
            autoCorrect={false}
            leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
            rightAdornment={
              categorySearch.length > 0 ? (
                <PressableScale onPress={() => setCategorySearch('')} hitSlop={8}>
                  <Icon name="close" size={16} color={colors.textMuted} />
                </PressableScale>
              ) : undefined
            }
          />
        </View>
        <ScrollView
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.lg }}
        >
          {filteredCategories.map((item, index) => {
            const itemId = item.id || item._id;
            const isSelected = categoryId === itemId;
            return (
              <PressableScale
                key={itemId || index.toString()}
                onPress={() => {
                  setCategoryId(itemId);
                  setCategoryName(item.title);
                  setErrors(prev => { const { category, ...rest } = prev; return rest; });
                  setCategoryModalVisible(false);
                }}
                accessibilityRole="button"
                accessibilityState={{ selected: isSelected }}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingVertical: spacing.md,
                }}
              >
                <CategoryAvatar category={item} />
                <View style={{ flex: 1 }}>
                  <Text variant="body" weight={isSelected ? '600' : '400'} color={isSelected ? 'accent' : 'textPrimary'}>
                    {item.title}
                  </Text>
                  {item.description ? (
                    <Text variant="caption" color="textMuted" numberOfLines={1}>
                      {item.description}
                    </Text>
                  ) : null}
                </View>
                {isSelected && <Icon name="checkCircle" size={22} color={colors.accent} />}
              </PressableScale>
            );
          })}
          {filteredCategories.length === 0 && (
            <View style={{ padding: spacing['3xl'], alignItems: 'center', gap: spacing.md }}>
              <Icon name="search" size={32} color={colors.textMuted} />
              <Text variant="bodySm" color="textMuted">
                {`No categories match "${categorySearch}"`}
              </Text>
            </View>
          )}
        </ScrollView>
      </Sheet>
    </SafeAreaView>
  );
};

export default AddProduct;
