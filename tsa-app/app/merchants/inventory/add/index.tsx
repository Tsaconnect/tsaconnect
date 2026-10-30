// app/merchants/inventory/add/index.tsx
//
// Add inventory product form (merchant). Restyled onto the TSA Signature design
// system (@/theme + @/components/ui). All logic is preserved verbatim: the
// getCategories / createProduct API calls, the expo-image-picker gallery/camera
// flow that appends LOCAL asset URIs and builds the multipart FormData (images
// are NOT pre-uploaded — they ride along on api.createProduct), the zone-rate
// auto-fill math (now in useMerchantPricing), attribute
// add/remove, the full validateForm() rule set, LocationPicker wiring, and the
// reset + router.back() success flow. Only the chrome changed: legacy GOLD/
// StyleSheet → tokens + primitives; the result Modal → InlineMessage (errors) + useToast (success); the category
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
import { appendListingLocation } from '@/utils/location';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import {
  Avatar,
  Button,
  Icon,
  InlineMessage,
  Input,
  PressableScale,
  SelectRow,
  Sheet,
  Spinner,
  Text,
  useToast,
} from '@/components/ui';
import { parseAmount, useMerchantPricing } from '@/hooks/useMerchantPricing';
import PromotionFields from '@/components/promotion/PromotionFields';
import { usePromotion } from '@/components/promotion/promoForm';
import {
  appendUsdAmounts,
  PriceInput,
  PricingCurrencySelect,
  ShippingRatesFields,
} from '@/components/products/MerchantPricingFields';
import { SchemaFields } from '@/components/forms/SchemaFields';
import { useCategoryListingFields } from '@/hooks/useCategoryListingFields';
import { cleanSchemaAnswers, validateSchemaAnswers } from '@/utils/schemaFields';

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
  const kbHeight = useKeyboardHeight();
  const toast = useToast();
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);
  const [categoriesError, setCategoriesError] = useState<string | null>(null);
  const [imageError, setImageError] = useState<string | null>(null);
  const [submitError, setSubmitError] = useState<string | null>(null);

  const pricing = useMerchantPricing();
  const promotion = usePromotion(pricing);

  const [productName, setProductName] = useState('');
  const [description, setDescription] = useState('');
  const [stock, setStock] = useState('0');
  const [categoryId, setCategoryId] = useState('');
  const [categoryName, setCategoryName] = useState('');
  const [location, setLocation] = useState({ country: "", state: "", city: "" });
  const [area, setArea] = useState('');
  const [phoneNumber, setPhoneNumber] = useState('');
  const [email, setEmail] = useState('');
  const [companyName, setCompanyName] = useState('');
  const [productImages, setProductImages] = useState<string[]>([]);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [categoriesLoading, setCategoriesLoading] = useState(true);
  const [categoryModalVisible, setCategoryModalVisible] = useState(false);
  const [categorySearch, setCategorySearch] = useState('');

  const [errors, setErrors] = useState<Record<string, string>>({});
  // Answers to the chosen category's listing fields (brand, …).
  const [listingValues, setListingValues] = useState<Record<string, any>>({});
  const [listingErrors, setListingErrors] = useState<Record<string, string>>({});
  const { fields: listingFields } = useCategoryListingFields(categoryId || null);

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
    setCategoriesError(null);
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
      setCategoriesError('Could not load categories. Check your connection and try again.');
    } finally {
      setCategoriesLoading(false);
    }
  };

  const pickProductImages = async () => {
    try {
      const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== 'granted') {
        setImageError('Allow photo library access in Settings to add product photos.');
        return;
      }

      const result = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        allowsMultipleSelection: true,
        aspect: [4, 3],
        quality: 0.8,
        selectionLimit: 10 - productImages.length,
      });

      setImageError(null);
      if (!result.canceled && result.assets.length > 0) {
        const newImages = result.assets.map(asset => asset.uri);
        setProductImages(prev => [...prev, ...newImages].slice(0, 10));
      }
    } catch (error) {
      setImageError('Could not open your photos. Try again.');
    }
  };

  const removeImage = (index: number) => {
    setProductImages(prev => prev.filter((_, i) => i !== index));
  };

  const takePhoto = async () => {
    try {
      const { status } = await ImagePicker.requestCameraPermissionsAsync();
      if (status !== 'granted') {
        setImageError('Allow camera access in Settings to take a product photo.');
        return;
      }

      const result = await ImagePicker.launchCameraAsync({
        allowsEditing: true,
        aspect: [4, 3],
        quality: 0.8,
      });

      setImageError(null);
      if (!result.canceled && result.assets[0]) {
        setProductImages(prev => [...prev, result.assets[0].uri].slice(0, 10));
      }
    } catch (error) {
      setImageError('Could not take the photo. Try again.');
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
    const price = pricing.amount('price');
    if (!pricing.ready) newErrors.price = 'Exchange rates are still loading. Try again in a moment.';
    else if (!price.trim()) newErrors.price = 'Price is required';
    else if (isNaN(parseAmount(price)) || parseAmount(price) <= 0) newErrors.price = 'Price must be a positive number';
    const promoError = promotion.validate(price);
    if (promoError) newErrors.promotion = promoError;
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

    const listingProblems = validateSchemaAnswers(listingFields, listingValues);
    setListingErrors(listingProblems);

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0 && Object.keys(listingProblems).length === 0;
  };

  const createProduct = async () => {
    if (!validateForm()) {
      setSubmitError('Some fields need attention. Fix the ones marked in red and publish again.');
      return;
    }

    setSubmitError(null);
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
      formData.append('stock', parseInt(stock).toString());
      formData.append('category', categoryId);
      appendListingLocation(formData, location, area);
      formData.append('phoneNumber', phoneNumber);
      formData.append('email', email);
      if (companyName.trim()) formData.append('companyName', companyName);
      appendUsdAmounts(formData, pricing);
      promotion.append(formData);
      if (attributes.length > 0) {
        formData.append('attributes', JSON.stringify(attributes));
      }
      if (listingFields.length > 0) {
        formData.append('listingValues', JSON.stringify(cleanSchemaAnswers(listingFields, listingValues)));
      }

      const response = await api.createProduct(formData);

      if (response.success) {
        toast.success('Product created');
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
      setSubmitError((error as any).message || 'Could not create the product. Try again.');
    } finally {
      setLoading(false);
    }
  };

  const resetForm = () => {
    setProductName('');
    setDescription('');
    pricing.reset();
    promotion.reset();
    setStock('0');
    setCategoryId('');
    setCategoryName('');
    setLocation({ country: "", state: "", city: "" });
    setArea('');
    setPhoneNumber('');
    setEmail('');
    setCompanyName('');
    setProductImages([]);
    setAttributes([]);
    setListingValues({});
    setListingErrors({});
    setErrors({});
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
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* "Add Product" is the header title now. */}
          <Text variant="bodySm" color="textSecondary">
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
          <InlineMessage message={imageError} onDismiss={() => setImageError(null)} style={{ marginBottom: spacing.sm }} />

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
                {categoriesError ? (
                  <InlineMessage message={categoriesError} actionLabel="Try again" onAction={fetchCategories} />
                ) : (
                  <PressableScale onPress={fetchCategories} style={{ paddingVertical: spacing.md }}>
                    <Text variant="bodySm" color="textMuted">
                      No categories found. Tap to retry.
                    </Text>
                  </PressableScale>
                )}
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

          {/* The category's own listing fields (set by admins per category). */}
          {listingFields.length > 0 && (
            <View style={{ marginBottom: spacing.lg, gap: spacing.md }}>
              <Text variant="bodyMed" weight="700">
                {categoryName || 'Category'} details
              </Text>
              <SchemaFields
                fields={listingFields}
                answers={listingValues}
                errors={listingErrors}
                onChange={(key, value) => {
                  setListingValues((prev) => ({ ...prev, [key]: value }));
                  setListingErrors((prev) => (prev[key] ? { ...prev, [key]: '' } : prev));
                }}
              />
            </View>
          )}

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

          <PricingCurrencySelect pricing={pricing} style={{ marginBottom: spacing.md }} />

          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <View style={{ flex: 1 }}>
              <PriceInput pricing={pricing} error={errors.price} />
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

          <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Promotion
          </Text>
          <PromotionFields
            promotion={promotion}
            originalPrice={pricing.amount('price')}
            currencySymbol={pricing.selectedCurrency.symbol}
            priceDisabled={!pricing.ready}
            error={errors.promotion}
          />

          {/* Shipping Rates */}
          <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
            Shipping Rates ({pricing.selectedCurrency.symbol})
          </Text>

          <View style={{ marginBottom: spacing.lg }}>
            <ShippingRatesFields pricing={pricing} />
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
            <Input
              label="Area or neighbourhood"
              value={area}
              onChangeText={setArea}
              placeholder="e.g. Odenigbo, Kubwa, Yaba"
              containerStyle={{ marginTop: spacing.md }}
            />
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

          <InlineMessage message={submitError} style={{ marginTop: spacing['2xl'] }} />
          <Button
            title="Publish Product"
            leftIcon="cart"
            onPress={createProduct}
            disabled={loading}
            loading={loading}
            size="lg"
            style={{ marginTop: submitError ? spacing.md : spacing['2xl'] }}
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
