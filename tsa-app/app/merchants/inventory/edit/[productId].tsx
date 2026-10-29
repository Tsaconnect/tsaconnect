// app/merchants/inventory/edit/[productId].tsx
//
// Edit inventory product — prefill an existing product (from a passed
// productData param or a getProductById fetch), edit every field, then
// updateProduct (multipart FormData) or deleteProduct. Restyled onto the TSA
// Signature design system (@/theme tokens + @/components/ui primitives).
//
// VISUAL/STRUCTURAL restyle ONLY. All logic is preserved verbatim: the prefill
// (loadProduct: productData-param JSON fast path → getProductById fallback,
// location string split, category id/name resolution, http-only existing image
// URL filter, attribute mapping), fetchCategories, the expo-image-picker
// gallery/camera flows (local URIs appended to FormData), validateForm, the
// updateProduct FormData assembly (existingImages JSON, new image multipart
// parts, name/description/price/stock/category/location/contact/shipping/
// attributes), handleDeleteProduct (Alert.alert destructive confirm), the
// shipping base-fee → zone auto-fill (now in useMerchantPricing), all
// refs/focus chaining, and the showModal-driven
// success/error/info dialog + post-update router.back() timing.
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  TextInput,
  ScrollView,
  Image,
  Modal,
  KeyboardAvoidingView,
  Platform,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router, useLocalSearchParams } from 'expo-router';
import * as ImagePicker from 'expo-image-picker';
import { useAuth } from '@/AuthContext/AuthContext';
import api, { Product } from '@/components/services/api';
import LocationPicker from '../../../../components/common/LocationPicker';
import { formatLocation, parseLocation } from '@/utils/location';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import {
  AccentCard,
  Avatar,
  Button,
  Card,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  Text,
} from '@/components/ui';
import { parseAmount, useMerchantPricing } from '@/hooks/useMerchantPricing';
import PromotionFields from '@/components/promotion/PromotionFields';
import { usePromotion } from '@/components/promotion/promoForm';
import { SchemaFields } from '@/components/forms/SchemaFields';
import { useCategoryListingFields } from '@/hooks/useCategoryListingFields';
import { cleanSchemaAnswers, validateSchemaAnswers } from '@/utils/schemaFields';
import {
  appendUsdAmounts,
  PriceInput,
  PricingCurrencySelect,
  ShippingRatesFields,
} from '@/components/products/MerchantPricingFields';

const EditProduct = () => {
  const { colors, spacing, radius } = useTheme();
  const kbHeight = useKeyboardHeight();
  const { productId, productData: productDataParam } = useLocalSearchParams<{
    productId: string;
    productData?: string;
  }>();
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);
  const [initialLoading, setInitialLoading] = useState(true);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [modalMessage, setModalMessage] = useState('');
  const [modalType, setModalType] = useState<'success' | 'error' | 'info'>('success');

  const pricing = useMerchantPricing();
  const promotion = usePromotion(pricing);

  const [productName, setProductName] = useState('');
  const [description, setDescription] = useState('');
  const [stock, setStock] = useState('0');
  const [categoryId, setCategoryId] = useState('');
  const [categoryName, setCategoryName] = useState('');
  const [location, setLocation] = useState({ country: '', state: '', city: '' });
  const [phoneNumber, setPhoneNumber] = useState('');
  const [email, setEmail] = useState('');
  const [companyName, setCompanyName] = useState('');
  const [newImages, setNewImages] = useState<string[]>([]);
  const [existingImageUrls, setExistingImageUrls] = useState<string[]>([]);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [categoriesLoading, setCategoriesLoading] = useState(true);
  const [categoryModalVisible, setCategoryModalVisible] = useState(false);
  const [categorySearch, setCategorySearch] = useState('');

  const [actualProductId, setActualProductId] = useState<string>('');
  const [errors, setErrors] = useState<Record<string, string>>({});
  // Answers to the category's listing fields; their own errors so field keys
  // can never collide with this form's error keys.
  const [listingValues, setListingValues] = useState<Record<string, any>>({});
  const [listingErrors, setListingErrors] = useState<Record<string, string>>({});
  const { fields: listingFields } = useCategoryListingFields(categoryId || null);
  const maxDescriptionLength = 500;

  const descriptionRef = useRef<TextInput>(null);
  const emailRef = useRef<TextInput>(null);
  const companyRef = useRef<TextInput>(null);
  const scrollRef = useRef<ScrollView>(null);

  useEffect(() => {
    if (token) {
      api.setToken(token.replace('Bearer ', ''));
    }
    loadProduct();
    fetchCategories();
  }, []);

  const loadProduct = async () => {
    try {
      let product: Product | null = null;

      if (productDataParam) {
        try {
          product = JSON.parse(productDataParam as string);
        } catch {}
      }

      if (!product && productId) {
        const response = await api.getProductById(productId as string);
        if (response.success && response.data) {
          product = response.data;
        }
      }

      if (product) {
        setActualProductId(product.id || product._id || (productId as string));
        setProductName(product.name || '');
        setDescription(product.description || '');
        setStock(product.stock?.toString() || '0');
        setLocation(parseLocation(product.location));
        setPhoneNumber(product.phoneNumber || '');
        setEmail(product.email || '');
        setCompanyName(product.companyName || '');
        pricing.loadSavedAmounts({
          price: product.price,
          shippingSameCity: product.shippingSameCity,
          shippingSameState: product.shippingSameState,
          shippingSameCountry: product.shippingSameCountry,
          shippingInternational: product.shippingInternational,
        });

        promotion.load(product);

        // Category
        const catId =
          typeof product.category === 'string'
            ? product.category
            : (product.category as any)?.id || (product.category as any)?._id || '';
        setCategoryId(catId);
        setCategoryName(product.categoryName || '');

        // Existing images with valid URLs
        if (product.images?.length) {
          const urls = product.images
            .filter((img: any) => img.url && img.url.startsWith('http'))
            .map((img: any) => img.url);
          setExistingImageUrls(urls);
        }

        const values = (product as any).listingValues;
        if (values && typeof values === 'object') setListingValues(values);

        // Attributes
        if (product.attributes?.length) {
          setAttributes(
            product.attributes.map((a: any) => ({
              name: a.name || '',
              value: a.value || '',
            }))
          );
        }
      }
    } catch (error) {
      console.error('Load product error:', error);
      showModal('error', 'Failed to load product');
    } finally {
      setInitialLoading(false);
    }
  };

  const fetchCategories = async () => {
    setCategoriesLoading(true);
    try {
      const response = await api.getCategories({ type: 'Product', active: true });
      if (response.success && response.data) {
        setCategories(response.data);
      }
    } catch (error) {
      console.error('Failed to load categories');
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
      const totalImages = existingImageUrls.length + newImages.length;
      const result = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        allowsMultipleSelection: true,
        aspect: [4, 3],
        quality: 0.8,
        selectionLimit: 10 - totalImages,
      });
      if (!result.canceled && result.assets.length > 0) {
        const picked = result.assets.map((asset) => asset.uri);
        setNewImages((prev) => [...prev, ...picked].slice(0, 10 - existingImageUrls.length));
      }
    } catch (error) {
      showModal('error', 'Failed to pick images');
    }
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
        setNewImages((prev) => [...prev, result.assets[0].uri].slice(0, 10 - existingImageUrls.length));
      }
    } catch (error) {
      showModal('error', 'Failed to take photo');
    }
  };

  const removeExistingImage = (index: number) => {
    setExistingImageUrls((prev) => prev.filter((_, i) => i !== index));
  };

  const removeNewImage = (index: number) => {
    setNewImages((prev) => prev.filter((_, i) => i !== index));
  };

  const handleAddAttribute = () => {
    setAttributes([...attributes, { name: '', value: '' }]);
  };

  const handleRemoveAttribute = (index: number) => {
    const updated = [...attributes];
    updated.splice(index, 1);
    setAttributes(updated);
  };

  const handleAttributeChange = (index: number, field: 'name' | 'value', value: string) => {
    const updated = [...attributes];
    updated[index][field] = value;
    setAttributes(updated);
  };

  const validateForm = () => {
    const newErrors: Record<string, string> = {};
    if (!productName.trim()) newErrors.productName = 'Product name is required';
    if (!description.trim()) newErrors.description = 'Description is required';
    if (description.length > maxDescriptionLength) newErrors.description = `Max ${maxDescriptionLength} characters`;
    const price = pricing.amount('price');
    if (!pricing.ready) newErrors.price = 'Exchange rates are still loading. Try again in a moment.';
    else if (!price.trim()) newErrors.price = 'Price is required';
    else if (isNaN(parseAmount(price)) || parseAmount(price) <= 0) newErrors.price = 'Price must be positive';
    const promoError = promotion.validate(price);
    if (promoError) newErrors.promotion = promoError;
    if (!stock.trim()) newErrors.stock = 'Stock is required';
    if (isNaN(parseInt(stock)) || parseInt(stock) < 0) newErrors.stock = 'Stock must be non-negative';
    if (!categoryId) newErrors.category = 'Category is required';
    if (!location.country) newErrors.location = 'Country is required';
    else if (!location.state) newErrors.location = 'State is required';
    if (!phoneNumber.trim()) newErrors.phoneNumber = 'Phone number is required';
    if (!email.trim()) newErrors.email = 'Email is required';
    if (email.trim() && !/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) newErrors.email = 'Invalid email';
    if (existingImageUrls.length === 0 && newImages.length === 0) newErrors.images = 'At least one image is required';

    attributes.forEach((attr, index) => {
      if (!attr.name.trim()) newErrors[`attr_name_${index}`] = 'Required';
      if (!attr.value.trim()) newErrors[`attr_value_${index}`] = 'Required';
    });

    const listingProblems = validateSchemaAnswers(listingFields, listingValues);
    setListingErrors(listingProblems);

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0 && Object.keys(listingProblems).length === 0;
  };

  const updateProduct = async () => {
    if (!validateForm()) {
      showModal('error', 'Please fix all errors before submitting');
      return;
    }

    setLoading(true);
    try {
      const formData = new FormData();

      // Send existing image URLs to keep
      if (existingImageUrls.length > 0) {
        formData.append('existingImages', JSON.stringify(existingImageUrls));
      }

      // Append new images
      newImages.forEach((uri, index) => {
        const filename = uri.split('/').pop() || `image_${index}.jpg`;
        const match = /\.(\w+)$/.exec(filename);
        const type = match ? `image/${match[1]}` : 'image/jpeg';
        //@ts-ignore
        formData.append('images', { uri, name: filename, type });
      });

      formData.append('name', productName);
      formData.append('description', description);
      formData.append('stock', parseInt(stock).toString());
      formData.append('category', categoryId);
      formData.append('location', formatLocation(location));
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

      const updateId = actualProductId || (productId as string);
      const response = await api.updateProduct(updateId, formData);

      if (response.success) {
        showModal('success', 'Product updated successfully!');
        setTimeout(() => {
          if (router.canGoBack()) {
            router.back();
          }
        }, 1500);
      } else {
        throw new Error(response.message || 'Failed to update product');
      }
    } catch (error) {
      console.error('Update product error:', error);
      showModal('error', (error as any).message || 'An error occurred');
    } finally {
      setLoading(false);
    }
  };

  const handleDeleteProduct = () => {
    Alert.alert(
      'Delete Product',
      'Are you sure you want to delete this product? This cannot be undone.',
      [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Delete',
          style: 'destructive',
          onPress: async () => {
            try {
              setLoading(true);
              const deleteId = actualProductId || (productId as string);
              const response = await api.deleteProduct(deleteId);
              if (response.success) {
                showModal('success', 'Product deleted');
                setTimeout(() => router.back(), 1500);
              } else {
                showModal('error', response.message || 'Failed to delete');
              }
            } catch (error) {
              showModal('error', 'Failed to delete product');
            } finally {
              setLoading(false);
            }
          },
        },
      ]
    );
  };

  const showModal = (type: 'success' | 'error' | 'info', message: string) => {
    setModalType(type);
    setModalMessage(message);
    setIsModalVisible(true);
  };

  const totalImages = existingImageUrls.length + newImages.length;

  // Visual helpers for the success/error/info dialog.
  const modalTone =
    modalType === 'success' ? colors.success : modalType === 'error' ? colors.danger : colors.warning;
  const modalToneBg =
    modalType === 'success' ? colors.successBg : modalType === 'error' ? colors.dangerBg : colors.warningBg;
  const modalIcon =
    modalType === 'success' ? 'checkCircle' : modalType === 'error' ? 'closeCircle' : 'info';

  const filteredCategories = categories.filter(
    (item) => !categorySearch || item.title?.toLowerCase().includes(categorySearch.toLowerCase())
  );

  if (initialLoading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading product..." />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
      >
        {/* Title and back live in the native header (app/merchants/_layout.tsx);
            only the trailing action belongs to this screen. */}
        <Stack.Screen
          options={{
            headerRight: () => (
              <IconButton
                icon="trash"
                accessibilityLabel="Delete product"
                color={colors.danger}
                disabled={loading}
                onPress={handleDeleteProduct}
              />
            ),
          }}
        />

        <ScrollView
          ref={scrollRef}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          keyboardShouldPersistTaps="handled"
        >
          {/* Basic Information */}
          <Card style={{ marginBottom: spacing.lg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="info" size={20} color={colors.accent} />
              <Text variant="bodyMed" weight="700">
                Basic Information
              </Text>
            </View>
            <Divider spacing="sm" />

            <View style={{ gap: spacing.md, marginTop: spacing.sm }}>
              <Input
                label="Product Name *"
                placeholder="Enter product name"
                value={productName}
                onChangeText={setProductName}
                returnKeyType="next"
                onSubmitEditing={() => descriptionRef.current?.focus()}
                error={errors.productName}
              />

              <Input
                ref={descriptionRef}
                label="Description *"
                placeholder="Describe your product..."
                value={description}
                onChangeText={setDescription}
                multiline
                numberOfLines={4}
                maxLength={maxDescriptionLength}
                counter
                inputStyle={{ minHeight: 80, textAlignVertical: 'top' } as any}
                error={errors.description}
              />

              <PricingCurrencySelect pricing={pricing} style={{ marginBottom: spacing.md }} />

              <View style={{ flexDirection: 'row', gap: spacing.md }}>
                <PriceInput pricing={pricing} error={errors.price} containerStyle={{ flex: 1 }} />
                <Input
                  containerStyle={{ flex: 1 }}
                  label="Stock *"
                  placeholder="0"
                  keyboardType="number-pad"
                  value={stock}
                  onChangeText={setStock}
                  error={errors.stock}
                />
              </View>
            </View>

            {/* Promotion */}
            <Text variant="bodySm" weight="700" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
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
            <Text variant="bodySm" weight="700" style={{ marginTop: spacing.lg, marginBottom: spacing.xs }}>
              Shipping Rates ({pricing.selectedCurrency.symbol})
            </Text>

            <ShippingRatesFields pricing={pricing} onCard />

            {/* Category Picker */}
            <View style={{ marginTop: spacing.md }}>
              <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
                Category *
              </Text>
              {categoriesLoading ? (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, paddingVertical: spacing.md }}>
                  <Spinner size="small" inline />
                  <Text variant="bodySm" color="textSecondary">
                    Loading categories...
                  </Text>
                </View>
              ) : (
                <PressableScale
                  onPress={() => {
                    setCategorySearch('');
                    setCategoryModalVisible(true);
                  }}
                  accessibilityRole="button"
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    gap: spacing.sm,
                    minHeight: 48,
                    paddingHorizontal: spacing.md,
                    borderRadius: radius.md,
                    borderWidth: 1.5,
                    borderColor: errors.category ? colors.danger : colors.border,
                    backgroundColor: colors.surface,
                  }}
                >
                  {categoryId ? (
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, flex: 1 }}>
                      <CategoryAvatar
                        category={categories.find((c) => (c.id || c._id) === categoryId) || { title: categoryName }}
                      />
                      <Text variant="body" weight="600" numberOfLines={1} style={{ flex: 1 }}>
                        {categoryName}
                      </Text>
                    </View>
                  ) : (
                    <Text variant="body" color="textMuted" style={{ flex: 1 }}>
                      Select a category
                    </Text>
                  )}
                  <Icon name="chevronDown" size={18} color={colors.textMuted} />
                </PressableScale>
              )}
              {errors.category ? (
                <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                  {errors.category}
                </Text>
              ) : null}
            </View>
          </Card>

          {/* Contact Information */}
          <Card style={{ marginBottom: spacing.lg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="mobile" size={20} color={colors.accent} />
              <Text variant="bodyMed" weight="700">
                Contact Information
              </Text>
            </View>
            <Divider spacing="sm" />

            <View style={{ gap: spacing.md, marginTop: spacing.sm }}>
              <View>
                <LocationPicker value={location} onChange={setLocation} required={['country', 'state']} />
                {errors.location ? (
                  <Text variant="caption" color="danger" style={{ marginTop: spacing.xs }}>
                    {errors.location}
                  </Text>
                ) : null}
              </View>

              <Input
                label="Phone Number *"
                placeholder="Enter phone number"
                keyboardType="phone-pad"
                value={phoneNumber}
                onChangeText={setPhoneNumber}
                returnKeyType="next"
                onSubmitEditing={() => emailRef.current?.focus()}
                error={errors.phoneNumber}
              />

              <Input
                ref={emailRef}
                label="Email *"
                placeholder="Enter email address"
                keyboardType="email-address"
                autoCapitalize="none"
                value={email}
                onChangeText={setEmail}
                returnKeyType="next"
                onSubmitEditing={() => companyRef.current?.focus()}
                error={errors.email}
              />

              <Input
                ref={companyRef}
                label="Company Name (Optional)"
                placeholder="Enter company name"
                value={companyName}
                onChangeText={setCompanyName}
                returnKeyType="done"
              />
            </View>
          </Card>

          {/* Images */}
          <Card style={{ marginBottom: spacing.lg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="image" size={20} color={colors.accent} />
              <Text variant="bodyMed" weight="700" style={{ flex: 1 }}>
                Product Images *
              </Text>
              <Text variant="caption" color="textSecondary">
                ({totalImages}/10)
              </Text>
            </View>
            <Divider spacing="sm" />

            {errors.images ? (
              <Text variant="caption" color="danger" style={{ marginTop: spacing.sm }}>
                {errors.images}
              </Text>
            ) : null}

            {/* Existing images */}
            {existingImageUrls.length > 0 && (
              <>
                <Text variant="caption" weight="600" color="textSecondary" style={{ marginTop: spacing.sm, marginBottom: spacing.xs }}>
                  Current Images
                </Text>
                <ScrollView
                  horizontal
                  showsHorizontalScrollIndicator={false}
                  contentContainerStyle={{ paddingVertical: spacing.xs, gap: spacing.sm }}
                >
                  {existingImageUrls.map((url, index) => (
                    <ImageTile key={`existing-${index}`} uri={url} onRemove={() => removeExistingImage(index)} />
                  ))}
                </ScrollView>
              </>
            )}

            {/* New images */}
            {newImages.length > 0 && (
              <>
                <Text variant="caption" weight="600" color="textSecondary" style={{ marginTop: spacing.sm, marginBottom: spacing.xs }}>
                  New Images
                </Text>
                <ScrollView
                  horizontal
                  showsHorizontalScrollIndicator={false}
                  contentContainerStyle={{ paddingVertical: spacing.xs, gap: spacing.sm }}
                >
                  {newImages.map((uri, index) => (
                    <ImageTile key={`new-${index}`} uri={uri} onRemove={() => removeNewImage(index)} />
                  ))}
                </ScrollView>
              </>
            )}

            {totalImages < 10 && (
              <View style={{ flexDirection: 'row', gap: spacing.md, marginTop: spacing.md }}>
                <Button
                  title="Gallery"
                  leftIcon="image"
                  variant="secondary"
                  onPress={pickProductImages}
                  style={{ flex: 1 }}
                />
                <Button
                  title="Camera"
                  leftIcon="camera"
                  variant="secondary"
                  onPress={takePhoto}
                  style={{ flex: 1 }}
                />
              </View>
            )}
          </Card>

          {/* The category's own listing fields (set by admins per category). */}
          {listingFields.length > 0 && (
            <Card style={{ marginBottom: spacing.lg }}>
              <Text variant="bodyMed" weight="700" style={{ marginBottom: spacing.md }}>
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
            </Card>
          )}

          {/* Attributes */}
          <Card style={{ marginBottom: spacing.lg }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.md }}>
              <Icon name="list" size={20} color={colors.accent} />
              <Text variant="bodyMed" weight="700" style={{ flex: 1 }}>
                Attributes (Optional)
              </Text>
              <IconButton icon="plus" accessibilityLabel="Add attribute" color={colors.accent} onPress={handleAddAttribute} />
            </View>
            <Divider spacing="sm" />

            {attributes.length > 0 ? (
              <View style={{ marginTop: spacing.sm }}>
                {attributes.map((item, index) => (
                  <View
                    key={index}
                    style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm, marginBottom: spacing.sm }}
                  >
                    <Input
                      containerStyle={{ flex: 1 }}
                      placeholder="e.g. Size"
                      value={item.name}
                      onChangeText={(value) => handleAttributeChange(index, 'name', value)}
                      error={errors[`attr_name_${index}`]}
                    />
                    <Input
                      containerStyle={{ flex: 1 }}
                      placeholder="e.g. Large"
                      value={item.value}
                      onChangeText={(value) => handleAttributeChange(index, 'value', value)}
                      error={errors[`attr_value_${index}`]}
                    />
                    <IconButton
                      icon="trash"
                      accessibilityLabel="Remove attribute"
                      color={colors.danger}
                      onPress={() => handleRemoveAttribute(index)}
                      style={{ marginTop: spacing.xs }}
                    />
                  </View>
                ))}
                <Button
                  title="Add Another"
                  leftIcon="plus"
                  variant="ghost"
                  onPress={handleAddAttribute}
                  style={{ marginTop: spacing.xs }}
                />
              </View>
            ) : (
              <Text variant="bodySm" color="textMuted" align="center" style={{ paddingVertical: spacing.sm }}>
                Add attributes like size, color, material, etc.
              </Text>
            )}
          </Card>

          {/* Submit */}
          <Button
            title="Save Changes"
            leftIcon="check"
            size="lg"
            loading={loading}
            disabled={loading}
            onPress={updateProduct}
          />
        </ScrollView>
      </KeyboardAvoidingView>

      {/* Category Sheet */}
      <Sheet visible={categoryModalVisible} onClose={() => setCategoryModalVisible(false)} title="Select Category">
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
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.md }}
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
                  setErrors((prev) => {
                    const { category, ...rest } = prev;
                    return rest;
                  });
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
                {isSelected && <Icon name="checkCircle" size={20} color={colors.accent} />}
              </PressableScale>
            );
          })}
        </ScrollView>
      </Sheet>

      {/* Success / error / info dialog */}
      <Modal
        visible={isModalVisible}
        transparent
        animationType="fade"
        onRequestClose={() => setIsModalVisible(false)}
      >
        <View
          style={{
            flex: 1,
            backgroundColor: colors.overlay,
            justifyContent: 'center',
            alignItems: 'center',
            paddingHorizontal: spacing.xl,
          }}
        >
          <Card
            padding="2xl"
            style={{ width: '100%', maxWidth: 400, alignItems: 'center' }}
          >
            <View
              style={{
                width: 72,
                height: 72,
                borderRadius: radius.pill,
                justifyContent: 'center',
                alignItems: 'center',
                marginBottom: spacing.lg,
                backgroundColor: modalToneBg,
              }}
            >
              <Icon name={modalIcon as any} size={40} color={modalTone} />
            </View>
            <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.xs }}>
              {modalType === 'success' ? 'Success!' : modalType === 'error' ? 'Error!' : 'Information'}
            </Text>
            <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing.xl }}>
              {modalMessage}
            </Text>
            <Button
              title={modalType === 'success' ? 'Continue' : 'Try Again'}
              onPress={() => setIsModalVisible(false)}
            />
          </Card>
        </View>
      </Modal>
    </SafeAreaView>
  );
};

// Category avatar — image (uri) or initial-tinted circle, via the Avatar primitive.
const CategoryAvatar = ({ category }: { category: any }) => {
  if (category?.icon) {
    return <Avatar uri={category.icon} size={40} />;
  }
  return <Avatar name={category?.title || '?'} size={40} />;
};

// A square image preview tile with a corner remove (×) button.
const ImageTile: React.FC<{ uri: string; onRemove: () => void }> = ({ uri, onRemove }) => {
  const { colors, radius, spacing } = useTheme();
  return (
    <View style={{ width: 90, height: 90 }}>
      <Image
        source={{ uri }}
        style={{ width: 90, height: 90, borderRadius: radius.md, borderWidth: 1, borderColor: colors.border }}
      />
      <PressableScale
        onPress={onRemove}
        accessibilityRole="button"
        accessibilityLabel="Remove image"
        hitSlop={6}
        style={{
          position: 'absolute',
          top: -spacing.xs,
          insetInlineEnd: -spacing.xs,
          width: 22,
          height: 22,
          borderRadius: radius.pill,
          alignItems: 'center',
          justifyContent: 'center',
          backgroundColor: colors.danger,
        }}
      >
        <Icon name="close" size={14} color="#FFFFFF" />
      </PressableScale>
    </View>
  );
};

export default EditProduct;
