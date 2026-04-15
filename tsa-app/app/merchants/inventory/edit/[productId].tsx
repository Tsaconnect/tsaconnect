import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TextInput,
  Pressable,
  ScrollView,
  StyleSheet,
  Image,
  Modal,
  KeyboardAvoidingView,
  Platform,
  Dimensions,
  ActivityIndicator,
  TouchableOpacity,
  Alert,
} from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import Icon from 'react-native-vector-icons/MaterialIcons';
import * as ImagePicker from 'expo-image-picker';
import { LinearGradient } from 'expo-linear-gradient';
import { useAuth } from '@/AuthContext/AuthContext';
import api, { Product } from '@/components/services/api';
import LocationPicker from "../../../../components/common/LocationPicker";
import { calculateZoneRates, PACKAGE_SIZE_PRESETS } from '@/constants/shipping';

const { width } = Dimensions.get('window');

const COLORS = {
  primary: '#8B4513',
  primaryLight: '#A0522D',
  primaryDark: '#654321',
  secondary: '#D2691E',
  accent: '#DEB887',
  background: '#F5F5DC',
  surface: '#FFF8DC',
  text: '#333333',
  textLight: '#666666',
  textLighter: '#999999',
  border: '#D2B48C',
  success: '#228B22',
  error: '#DC143C',
  warning: '#FF8C00',
  white: '#FFFFFF',
  gray: '#E8E8E8',
  darkGray: '#A9A9A9',
};

const CategoryAvatar = ({ category }: { category: any }) => {
  if (category.icon) {
    return (
      <Image source={{ uri: category.icon }} style={styles.categoryItemImage} />
    );
  }
  const initial = (category.title || '?')[0].toUpperCase();
  const bgColor = category.color || COLORS.primary;
  return (
    <View style={[styles.categoryItemAvatar, { backgroundColor: bgColor }]}>
      <Text style={styles.categoryItemAvatarText}>{initial}</Text>
    </View>
  );
};

const EditProduct = () => {
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
  const [newImages, setNewImages] = useState<string[]>([]);
  const [existingImageUrls, setExistingImageUrls] = useState<string[]>([]);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const [categories, setCategories] = useState<any[]>([]);
  const [categoriesLoading, setCategoriesLoading] = useState(true);
  const [categoryModalVisible, setCategoryModalVisible] = useState(false);
  const [categorySearch, setCategorySearch] = useState('');

  const [actualProductId, setActualProductId] = useState<string>('');
  const [errors, setErrors] = useState<Record<string, string>>({});
  const maxDescriptionLength = 500;

  const descriptionRef = useRef<TextInput>(null);
  const priceRef = useRef<TextInput>(null);
  const stockRef = useRef<TextInput>(null);
  const locationRef = useRef<TextInput>(null);
  const phoneRef = useRef<TextInput>(null);
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
        setActualProductId(product.id || product._id || productId as string);
        setProductName(product.name || '');
        setDescription(product.description || '');
        setPrice(product.price?.toString() || '');
        setStock(product.stock?.toString() || '0');
        const parts = (product.location || "").split(", ");
        setLocation({
          city: parts.length >= 3 ? parts[0] : "",
          state: parts.length >= 2 ? parts[parts.length - 2] : "",
          country: parts.length >= 1 ? parts[parts.length - 1] : "",
        });
        setPhoneNumber(product.phoneNumber || '');
        setEmail(product.email || '');
        setCompanyName(product.companyName || '');
        setShippingSameCity(product.shippingSameCity?.toString() || '');
        setShippingSameState(product.shippingSameState?.toString() || '');
        setShippingSameCountry(product.shippingSameCountry?.toString() || '');
        setShippingInternational(product.shippingInternational?.toString() || '');

        // Category
        const catId = typeof product.category === 'string' ? product.category :
          (product.category as any)?.id || (product.category as any)?._id || '';
        setCategoryId(catId);
        setCategoryName(product.categoryName || '');

        // Existing images with valid URLs
        if (product.images?.length) {
          const urls = product.images
            .filter((img: any) => img.url && img.url.startsWith('http'))
            .map((img: any) => img.url);
          setExistingImageUrls(urls);
        }

        // Attributes
        if (product.attributes?.length) {
          setAttributes(product.attributes.map((a: any) => ({
            name: a.name || '',
            value: a.value || '',
          })));
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
        const picked = result.assets.map(asset => asset.uri);
        setNewImages(prev => [...prev, ...picked].slice(0, 10 - existingImageUrls.length));
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
        setNewImages(prev => [...prev, result.assets[0].uri].slice(0, 10 - existingImageUrls.length));
      }
    } catch (error) {
      showModal('error', 'Failed to take photo');
    }
  };

  const removeExistingImage = (index: number) => {
    setExistingImageUrls(prev => prev.filter((_, i) => i !== index));
  };

  const removeNewImage = (index: number) => {
    setNewImages(prev => prev.filter((_, i) => i !== index));
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
    if (!price.trim()) newErrors.price = 'Price is required';
    if (isNaN(parseFloat(price)) || parseFloat(price) <= 0) newErrors.price = 'Price must be positive';
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

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
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

      const updateId = actualProductId || productId as string;
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
              const deleteId = actualProductId || productId as string;
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

  if (initialLoading) {
    return (
      <View style={styles.loadingContainer}>
        <ActivityIndicator size="large" color={COLORS.primary} />
        <Text style={styles.loadingText}>Loading product...</Text>
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      style={styles.container}
      keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
    >
      <LinearGradient
        colors={[COLORS.background, COLORS.surface]}
        style={styles.gradientBackground}
      >
        <ScrollView
          ref={scrollRef}
          style={styles.scrollView}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={styles.scrollContent}
          keyboardShouldPersistTaps="handled"
        >
          {/* Header */}
          <View style={styles.header}>
            <Pressable style={styles.backButton} onPress={() => router.back()}>
              <Icon name="arrow-back" size={28} color={COLORS.primary} />
            </Pressable>
            <Text style={styles.headerTitle}>Edit Product</Text>
            <Pressable style={styles.deleteButton} onPress={handleDeleteProduct} disabled={loading}>
              <Icon name="delete" size={24} color={COLORS.error} />
            </Pressable>
          </View>

          <View style={styles.formContainer}>
            {/* Basic Information */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="info" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Basic Information</Text>
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Product Name *</Text>
                <TextInput
                  style={[styles.input, errors.productName && styles.inputError]}
                  placeholder="Enter product name"
                  placeholderTextColor={COLORS.textLighter}
                  value={productName}
                  onChangeText={setProductName}
                  returnKeyType="next"
                  onSubmitEditing={() => descriptionRef.current?.focus()}
                />
                {errors.productName && <Text style={styles.errorText}>{errors.productName}</Text>}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Description *</Text>
                <View style={[styles.textAreaContainer, errors.description && styles.inputError]}>
                  <TextInput
                    ref={descriptionRef}
                    style={styles.textArea}
                    placeholder="Describe your product..."
                    placeholderTextColor={COLORS.textLighter}
                    multiline
                    numberOfLines={4}
                    value={description}
                    onChangeText={setDescription}
                    maxLength={maxDescriptionLength}
                  />
                  <Text style={styles.charCount}>{description.length}/{maxDescriptionLength}</Text>
                </View>
                {errors.description && <Text style={styles.errorText}>{errors.description}</Text>}
              </View>

              <View style={styles.row}>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Price ($) *</Text>
                  <TextInput
                    ref={priceRef}
                    style={[styles.input, errors.price && styles.inputError]}
                    placeholder="0.00"
                    placeholderTextColor={COLORS.textLighter}
                    keyboardType="decimal-pad"
                    value={price}
                    onChangeText={setPrice}
                  />
                  {errors.price && <Text style={styles.errorText}>{errors.price}</Text>}
                  <Text style={styles.feeHint}>2% platform fee applies per sale</Text>
                </View>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Stock *</Text>
                  <TextInput
                    ref={stockRef}
                    style={[styles.input, errors.stock && styles.inputError]}
                    placeholder="0"
                    placeholderTextColor={COLORS.textLighter}
                    keyboardType="number-pad"
                    value={stock}
                    onChangeText={setStock}
                  />
                  {errors.stock && <Text style={styles.errorText}>{errors.stock}</Text>}
                </View>
              </View>

              {/* Shipping Rates */}
              <Text style={[styles.label, { marginTop: 8, marginBottom: 4, fontWeight: '700' }]}>Shipping Rates ($)</Text>

              <Text style={[styles.label, { marginTop: 4, marginBottom: 6 }]}>Package size (quick-select)</Text>
              <View style={{ flexDirection: 'row', gap: 8, marginBottom: 14, flexWrap: 'wrap' }}>
                {PACKAGE_SIZE_PRESETS.map((preset) => (
                  <TouchableOpacity
                    key={preset.key}
                    style={{ flex: 1, minWidth: 90, paddingVertical: 10, paddingHorizontal: 10, borderRadius: 12, borderWidth: 1, borderColor: '#E0E0E0', backgroundColor: '#FAFAFA', alignItems: 'center' }}
                    onPress={() => applyPackagePreset(preset.baseFee)}
                  >
                    <Text style={{ fontSize: 12, fontWeight: '700', color: '#1A1A1A', textAlign: 'center' }}>{preset.label}</Text>
                    <Text style={{ fontSize: 11, color: COLORS.primary, marginTop: 2, fontWeight: '600' }}>${preset.baseFee}</Text>
                  </TouchableOpacity>
                ))}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Base shipping fee ($)</Text>
                <TextInput
                  style={styles.input}
                  placeholder="e.g. 2.00"
                  placeholderTextColor={COLORS.textLighter}
                  keyboardType="decimal-pad"
                  value={baseShippingFee}
                  onChangeText={applyBaseShippingFee}
                />
                <Text style={{ fontSize: 11, color: '#888', marginTop: 4, fontStyle: 'italic' }}>
                  Auto-fills zone rates below. Edit any zone to override.
                </Text>
              </View>

              <View style={styles.row}>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Same Residential Area</Text>
                  <TextInput style={[styles.input, { backgroundColor: '#f0f0f0', color: '#888' }]} value="Free" editable={false} />
                </View>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Same City</Text>
                  <TextInput style={styles.input} placeholder="0.00" placeholderTextColor={COLORS.textLighter} keyboardType="decimal-pad" value={shippingSameCity} onChangeText={setShippingSameCity} />
                </View>
              </View>
              <View style={styles.row}>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Same State</Text>
                  <TextInput style={styles.input} placeholder="0.00" placeholderTextColor={COLORS.textLighter} keyboardType="decimal-pad" value={shippingSameState} onChangeText={setShippingSameState} />
                </View>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Same Country</Text>
                  <TextInput style={styles.input} placeholder="0.00" placeholderTextColor={COLORS.textLighter} keyboardType="decimal-pad" value={shippingSameCountry} onChangeText={setShippingSameCountry} />
                </View>
              </View>
              <View style={styles.row}>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>International</Text>
                  <TextInput style={styles.input} placeholder="0.00" placeholderTextColor={COLORS.textLighter} keyboardType="decimal-pad" value={shippingInternational} onChangeText={setShippingInternational} />
                </View>
              </View>

              {/* Category Picker */}
              <View style={styles.formGroup}>
                <Text style={styles.label}>Category *</Text>
                {categoriesLoading ? (
                  <View style={styles.categoryLoadingRow}>
                    <ActivityIndicator size="small" color={COLORS.primary} />
                    <Text style={styles.categoryLoadingText}>Loading categories...</Text>
                  </View>
                ) : (
                  <TouchableOpacity
                    activeOpacity={0.7}
                    style={[
                      styles.categorySelector,
                      errors.category && styles.categorySelectorError,
                      categoryId ? styles.categorySelectorSelected : undefined,
                    ]}
                    onPress={() => {
                      setCategorySearch('');
                      setCategoryModalVisible(true);
                    }}
                  >
                    {categoryId ? (
                      <View style={styles.categorySelectorValue}>
                        <CategoryAvatar category={categories.find(c => (c.id || c._id) === categoryId) || { title: categoryName }} />
                        <Text style={styles.categorySelectorText}>{categoryName}</Text>
                      </View>
                    ) : (
                      <Text style={styles.categorySelectorPlaceholder}>Select a category</Text>
                    )}
                    <Icon name="keyboard-arrow-down" size={24} color={COLORS.textLight} />
                  </TouchableOpacity>
                )}
                {errors.category && <Text style={styles.errorText}>{errors.category}</Text>}
              </View>

              {/* Category Modal */}
              <Modal
                visible={categoryModalVisible}
                animationType="slide"
                transparent
                onRequestClose={() => setCategoryModalVisible(false)}
              >
                <View style={styles.pickerOverlay}>
                  <Pressable style={styles.pickerOverlayDismiss} onPress={() => setCategoryModalVisible(false)} />
                  <View style={styles.pickerModal}>
                    <View style={styles.pickerModalHeader}>
                      <Text style={styles.pickerModalTitle}>Select Category</Text>
                      <TouchableOpacity onPress={() => setCategoryModalVisible(false)}>
                        <Icon name="close" size={24} color={COLORS.textLight} />
                      </TouchableOpacity>
                    </View>
                    <View style={styles.categorySearchContainer}>
                      <Icon name="search" size={20} color={COLORS.textLight} />
                      <TextInput
                        style={styles.categorySearchInput}
                        placeholder="Search categories..."
                        placeholderTextColor={COLORS.textLighter}
                        value={categorySearch}
                        onChangeText={setCategorySearch}
                        autoCapitalize="none"
                      />
                      {categorySearch.length > 0 && (
                        <TouchableOpacity onPress={() => setCategorySearch('')}>
                          <Icon name="close" size={18} color={COLORS.textLight} />
                        </TouchableOpacity>
                      )}
                    </View>
                    <ScrollView keyboardShouldPersistTaps="handled">
                      {categories
                        .filter(item => !categorySearch || item.title.toLowerCase().includes(categorySearch.toLowerCase()))
                        .map((item, index) => {
                          const itemId = item.id || item._id;
                          const isSelected = categoryId === itemId;
                          return (
                            <TouchableOpacity
                              key={itemId || index.toString()}
                              style={[styles.categoryListItem, isSelected && styles.categoryListItemSelected]}
                              onPress={() => {
                                setCategoryId(itemId);
                                setCategoryName(item.title);
                                setErrors(prev => { const { category, ...rest } = prev; return rest; });
                                setCategoryModalVisible(false);
                              }}
                            >
                              <CategoryAvatar category={item} />
                              <View style={styles.categoryListItemInfo}>
                                <Text style={[styles.categoryListItemTitle, isSelected && styles.categoryListItemTitleSelected]}>
                                  {item.title}
                                </Text>
                                {item.description ? (
                                  <Text style={styles.categoryListItemDesc} numberOfLines={1}>{item.description}</Text>
                                ) : null}
                              </View>
                              {isSelected && <Icon name="check-circle" size={22} color={COLORS.primary} />}
                            </TouchableOpacity>
                          );
                        })}
                    </ScrollView>
                  </View>
                </View>
              </Modal>
            </View>

            {/* Contact Information */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="contact-phone" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Contact Information</Text>
              </View>

              <View style={styles.formGroup}>
                <LocationPicker
                  value={location}
                  onChange={setLocation}
                  required={["country", "state"]}
                />
                {errors.location && <Text style={styles.errorText}>{errors.location}</Text>}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Phone Number *</Text>
                <TextInput
                  ref={phoneRef}
                  style={[styles.input, errors.phoneNumber && styles.inputError]}
                  placeholder="Enter phone number"
                  placeholderTextColor={COLORS.textLighter}
                  keyboardType="phone-pad"
                  value={phoneNumber}
                  onChangeText={setPhoneNumber}
                  returnKeyType="next"
                  onSubmitEditing={() => emailRef.current?.focus()}
                />
                {errors.phoneNumber && <Text style={styles.errorText}>{errors.phoneNumber}</Text>}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Email *</Text>
                <TextInput
                  ref={emailRef}
                  style={[styles.input, errors.email && styles.inputError]}
                  placeholder="Enter email address"
                  placeholderTextColor={COLORS.textLighter}
                  keyboardType="email-address"
                  autoCapitalize="none"
                  value={email}
                  onChangeText={setEmail}
                  returnKeyType="next"
                  onSubmitEditing={() => companyRef.current?.focus()}
                />
                {errors.email && <Text style={styles.errorText}>{errors.email}</Text>}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Company Name (Optional)</Text>
                <TextInput
                  ref={companyRef}
                  style={styles.input}
                  placeholder="Enter company name"
                  placeholderTextColor={COLORS.textLighter}
                  value={companyName}
                  onChangeText={setCompanyName}
                  returnKeyType="done"
                />
              </View>
            </View>

            {/* Images */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="photo-library" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Product Images *</Text>
                <Text style={styles.imageCount}>({totalImages}/10)</Text>
              </View>

              {errors.images && <Text style={styles.errorText}>{errors.images}</Text>}

              {/* Existing images */}
              {existingImageUrls.length > 0 && (
                <>
                  <Text style={styles.imageSectionLabel}>Current Images</Text>
                  <ScrollView
                    horizontal
                    showsHorizontalScrollIndicator={false}
                    style={styles.imagesScroll}
                    contentContainerStyle={styles.imagesList}
                  >
                    {existingImageUrls.map((url, index) => (
                      <View key={`existing-${index}`} style={styles.imageItem}>
                        <Image source={{ uri: url }} style={styles.productImage} />
                        <Pressable style={styles.removeImageButton} onPress={() => removeExistingImage(index)}>
                          <Icon name="close" size={16} color={COLORS.white} />
                        </Pressable>
                      </View>
                    ))}
                  </ScrollView>
                </>
              )}

              {/* New images */}
              {newImages.length > 0 && (
                <>
                  <Text style={styles.imageSectionLabel}>New Images</Text>
                  <ScrollView
                    horizontal
                    showsHorizontalScrollIndicator={false}
                    style={styles.imagesScroll}
                    contentContainerStyle={styles.imagesList}
                  >
                    {newImages.map((uri, index) => (
                      <View key={`new-${index}`} style={styles.imageItem}>
                        <Image source={{ uri }} style={styles.productImage} />
                        <Pressable style={styles.removeImageButton} onPress={() => removeNewImage(index)}>
                          <Icon name="close" size={16} color={COLORS.white} />
                        </Pressable>
                      </View>
                    ))}
                  </ScrollView>
                </>
              )}

              {totalImages < 10 && (
                <View style={styles.imageUploadRow}>
                  <Pressable style={styles.uploadBtn} onPress={pickProductImages}>
                    <Icon name="photo-library" size={22} color={COLORS.white} />
                    <Text style={styles.uploadBtnText}>Gallery</Text>
                  </Pressable>
                  <Pressable style={[styles.uploadBtn, { backgroundColor: COLORS.secondary }]} onPress={takePhoto}>
                    <Icon name="camera-alt" size={22} color={COLORS.white} />
                    <Text style={styles.uploadBtnText}>Camera</Text>
                  </Pressable>
                </View>
              )}
            </View>

            {/* Attributes */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="tune" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Attributes (Optional)</Text>
                <Pressable style={styles.addAttributeHeaderButton} onPress={handleAddAttribute}>
                  <Icon name="add" size={24} color={COLORS.primary} />
                </Pressable>
              </View>

              {attributes.length > 0 ? (
                <>
                  {attributes.map((item, index) => (
                    <View key={index} style={styles.attributeItem}>
                      <View style={styles.attributeInputs}>
                        <TextInput
                          style={[styles.attributeInput, errors[`attr_name_${index}`] && styles.inputError]}
                          placeholder="e.g. Size"
                          placeholderTextColor={COLORS.textLighter}
                          value={item.name}
                          onChangeText={(value) => handleAttributeChange(index, 'name', value)}
                        />
                        <TextInput
                          style={[styles.attributeInput, errors[`attr_value_${index}`] && styles.inputError]}
                          placeholder="e.g. Large"
                          placeholderTextColor={COLORS.textLighter}
                          value={item.value}
                          onChangeText={(value) => handleAttributeChange(index, 'value', value)}
                        />
                      </View>
                      <Pressable style={styles.removeAttributeButton} onPress={() => handleRemoveAttribute(index)}>
                        <Icon name="delete" size={24} color={COLORS.error} />
                      </Pressable>
                    </View>
                  ))}
                  <Pressable style={styles.addAttributeButton} onPress={handleAddAttribute}>
                    <Icon name="add-circle" size={20} color={COLORS.primary} />
                    <Text style={styles.addAttributeButtonText}>Add Another</Text>
                  </Pressable>
                </>
              ) : (
                <Text style={styles.noAttributesHint}>
                  Add attributes like size, color, material, etc.
                </Text>
              )}
            </View>

            {/* Submit */}
            <Pressable
              style={[styles.submitButton, loading && styles.submitButtonDisabled]}
              onPress={updateProduct}
              disabled={loading}
            >
              <LinearGradient
                colors={[COLORS.primary, COLORS.primaryDark]}
                style={styles.submitButtonGradient}
                start={{ x: 0, y: 0 }}
                end={{ x: 1, y: 0 }}
              >
                {loading ? (
                  <ActivityIndicator color={COLORS.white} size="small" />
                ) : (
                  <>
                    <Icon name="save" size={24} color={COLORS.white} />
                    <Text style={styles.submitButtonText}>Save Changes</Text>
                  </>
                )}
              </LinearGradient>
            </Pressable>
          </View>
        </ScrollView>
      </LinearGradient>

      {/* Modal */}
      <Modal
        visible={isModalVisible}
        transparent
        animationType="fade"
        onRequestClose={() => setIsModalVisible(false)}
      >
        <View style={styles.modalOverlay}>
          <View style={styles.modalContainer}>
            <View style={[
              styles.modalIconContainer,
              modalType === 'success' && styles.modalIconSuccess,
              modalType === 'error' && styles.modalIconError,
              modalType === 'info' && styles.modalIconInfo,
            ]}>
              <Icon
                name={modalType === 'success' ? 'check-circle' : modalType === 'error' ? 'error' : 'info'}
                size={48}
                color={COLORS.white}
              />
            </View>
            <Text style={styles.modalTitle}>
              {modalType === 'success' ? 'Success!' : modalType === 'error' ? 'Error!' : 'Information'}
            </Text>
            <Text style={styles.modalMessage}>{modalMessage}</Text>
            <Pressable style={styles.modalButton} onPress={() => setIsModalVisible(false)}>
              <Text style={styles.modalButtonText}>
                {modalType === 'success' ? 'Continue' : 'Try Again'}
              </Text>
            </Pressable>
          </View>
        </View>
      </Modal>
    </KeyboardAvoidingView>
  );
};

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: COLORS.background },
  gradientBackground: { flex: 1 },
  scrollView: { flex: 1 },
  scrollContent: { paddingBottom: 40 },
  loadingContainer: { flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: COLORS.background },
  loadingText: { marginTop: 12, fontSize: 16, color: COLORS.textLight },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 60 : 20,
    paddingBottom: 16,
    backgroundColor: COLORS.white,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.border,
    elevation: 3,
  },
  backButton: { padding: 8, borderRadius: 20, backgroundColor: COLORS.accent + '20' },
  deleteButton: { padding: 8, borderRadius: 20, backgroundColor: COLORS.error + '15' },
  headerTitle: { fontSize: 20, fontWeight: '700', color: COLORS.primary, flex: 1, textAlign: 'center' },
  formContainer: { paddingHorizontal: 16, paddingTop: 16 },
  card: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 16,
    marginBottom: 16,
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.08,
    shadowRadius: 8,
    elevation: 3,
    borderWidth: 1,
    borderColor: COLORS.border + '30',
  },
  cardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 16,
    paddingBottom: 10,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.border + '30',
  },
  cardTitle: { fontSize: 16, fontWeight: '600', color: COLORS.primary, marginLeft: 10, flex: 1 },
  imageCount: { fontSize: 14, fontWeight: '500', color: COLORS.textLight },
  formGroup: { marginBottom: 14 },
  label: { fontSize: 14, fontWeight: '600', color: COLORS.primaryDark, marginBottom: 6 },
  input: {
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 12,
    fontSize: 15,
    color: COLORS.text,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
  },
  inputError: { borderColor: COLORS.error, backgroundColor: COLORS.error + '10' },
  errorText: { fontSize: 12, color: COLORS.error, marginTop: 4, marginLeft: 4 },
  feeHint: { fontSize: 11, color: '#888', marginTop: 4, marginLeft: 4 },
  textAreaContainer: {
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
    minHeight: 100,
  },
  textArea: { padding: 14, fontSize: 15, color: COLORS.text, textAlignVertical: 'top', minHeight: 80 },
  charCount: { textAlign: 'right', fontSize: 12, color: COLORS.textLight, paddingHorizontal: 14, paddingBottom: 8 },
  row: { flexDirection: 'row', justifyContent: 'space-between' },
  halfWidth: { width: '48%' },
  categorySelector: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: COLORS.border + '50',
    paddingHorizontal: 14,
    paddingVertical: 14,
  },
  categorySelectorSelected: { borderColor: COLORS.primary + '40', backgroundColor: COLORS.primary + '08' },
  categorySelectorError: { borderColor: '#ef4444' },
  categorySelectorValue: { flexDirection: 'row', alignItems: 'center', gap: 10, flex: 1 },
  categorySelectorText: { fontSize: 15, fontWeight: '600', color: COLORS.text },
  categorySelectorPlaceholder: { fontSize: 15, color: COLORS.textLighter },
  categorySearchContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: COLORS.gray,
    marginHorizontal: 16,
    marginVertical: 12,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: Platform.OS === 'ios' ? 10 : 4,
    gap: 8,
  },
  categorySearchInput: { flex: 1, fontSize: 15, color: COLORS.text },
  categoryLoadingRow: { flexDirection: 'row', alignItems: 'center', paddingVertical: 12 },
  categoryLoadingText: { fontSize: 14, color: COLORS.textLight, marginLeft: 8 },
  pickerOverlay: { flex: 1, backgroundColor: 'rgba(0,0,0,0.4)', justifyContent: 'flex-end' },
  pickerOverlayDismiss: { flex: 1 },
  pickerModal: {
    backgroundColor: COLORS.white,
    borderTopLeftRadius: 20,
    borderTopRightRadius: 20,
    maxHeight: '60%',
    paddingBottom: Platform.OS === 'ios' ? 30 : 16,
  },
  pickerModalHeader: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    padding: 16,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.gray,
  },
  pickerModalTitle: { fontSize: 18, fontWeight: '700', color: COLORS.primaryDark },
  categoryListItem: { flexDirection: 'row', alignItems: 'center', paddingHorizontal: 16, paddingVertical: 12 },
  categoryListItemSelected: { backgroundColor: COLORS.accent + '20' },
  categoryItemImage: { width: 40, height: 40, borderRadius: 20, backgroundColor: COLORS.gray },
  categoryItemAvatar: { width: 40, height: 40, borderRadius: 20, justifyContent: 'center', alignItems: 'center' },
  categoryItemAvatarText: { fontSize: 18, fontWeight: '700', color: COLORS.white },
  categoryListItemInfo: { flex: 1, marginLeft: 12 },
  categoryListItemTitle: { fontSize: 15, fontWeight: '500', color: COLORS.text },
  categoryListItemTitleSelected: { color: COLORS.primary, fontWeight: '600' },
  categoryListItemDesc: { fontSize: 12, color: COLORS.textLighter, marginTop: 2 },
  imageSectionLabel: { fontSize: 12, fontWeight: '600', color: COLORS.textLight, marginTop: 4, marginBottom: 4 },
  imageUploadRow: { flexDirection: 'row', gap: 12, marginTop: 8 },
  uploadBtn: {
    flex: 1,
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: COLORS.primary,
    paddingVertical: 12,
    borderRadius: 12,
    gap: 8,
  },
  uploadBtnText: { fontSize: 14, fontWeight: '600', color: COLORS.white },
  imagesScroll: { marginBottom: 8 },
  imagesList: { paddingVertical: 4, gap: 10 },
  imageItem: { position: 'relative' },
  productImage: { width: 90, height: 90, borderRadius: 10, borderWidth: 1, borderColor: COLORS.border },
  removeImageButton: {
    position: 'absolute',
    top: -6,
    right: -6,
    backgroundColor: COLORS.error,
    borderRadius: 10,
    width: 22,
    height: 22,
    justifyContent: 'center',
    alignItems: 'center',
  },
  attributeItem: { flexDirection: 'row', alignItems: 'center', marginBottom: 10 },
  attributeInputs: { flex: 1, flexDirection: 'row', gap: 8 },
  attributeInput: {
    flex: 1,
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: 10,
    fontSize: 14,
    color: COLORS.text,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
  },
  removeAttributeButton: { padding: 8, marginLeft: 4 },
  addAttributeHeaderButton: { padding: 4, borderRadius: 12, backgroundColor: COLORS.accent + '20' },
  noAttributesHint: { fontSize: 13, color: COLORS.textLighter, textAlign: 'center', paddingVertical: 8 },
  addAttributeButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: COLORS.accent + '20',
    borderRadius: 10,
    paddingVertical: 10,
    marginTop: 4,
    gap: 6,
  },
  addAttributeButtonText: { fontSize: 14, fontWeight: '600', color: COLORS.primary },
  submitButton: { borderRadius: 16, overflow: 'hidden', marginTop: 8, marginBottom: 16, elevation: 4 },
  submitButtonDisabled: { opacity: 0.7 },
  submitButtonGradient: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 16,
    gap: 10,
  },
  submitButtonText: { fontSize: 17, fontWeight: '700', color: COLORS.white },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContainer: {
    backgroundColor: COLORS.white,
    borderRadius: 20,
    padding: 28,
    width: '100%',
    maxWidth: 400,
    alignItems: 'center',
  },
  modalIconContainer: { width: 72, height: 72, borderRadius: 36, justifyContent: 'center', alignItems: 'center', marginBottom: 16 },
  modalIconSuccess: { backgroundColor: COLORS.success },
  modalIconError: { backgroundColor: COLORS.error },
  modalIconInfo: { backgroundColor: COLORS.warning },
  modalTitle: { fontSize: 22, fontWeight: '700', color: COLORS.primary, marginBottom: 8 },
  modalMessage: { fontSize: 15, color: COLORS.text, textAlign: 'center', marginBottom: 20, lineHeight: 22 },
  modalButton: { backgroundColor: COLORS.primary, borderRadius: 12, paddingHorizontal: 28, paddingVertical: 12, width: '100%' },
  modalButtonText: { fontSize: 15, fontWeight: '600', color: COLORS.white, textAlign: 'center' },
});

export default EditProduct;
