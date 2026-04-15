import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  Text,
  TextInput,
  ScrollView,
  StyleSheet,
  Image,
  Modal,
  KeyboardAvoidingView,
  Platform,
  ActivityIndicator,
  TouchableOpacity,
  Pressable,
} from 'react-native';
import { router } from 'expo-router';
import Icon from 'react-native-vector-icons/MaterialIcons';
import * as ImagePicker from 'expo-image-picker';
import { useAuth } from '@/AuthContext/AuthContext';
import api from '@/components/services/api';
import LocationPicker from "../../../../components/common/LocationPicker";
import { calculateZoneRates, PACKAGE_SIZE_PRESETS } from '@/constants/shipping';

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  bg: '#FAF9F6',
};

const CategoryAvatar = ({ category }: { category: any }) => {
  if (category.icon) {
    return (
      <Image source={{ uri: category.icon }} style={styles.categoryItemImage} />
    );
  }
  const initial = (category.title || '?')[0].toUpperCase();
  const bgColor = category.color || GOLD.dark;
  return (
    <View style={[styles.categoryItemAvatar, { backgroundColor: bgColor }]}>
      <Text style={styles.categoryItemAvatarText}>{initial}</Text>
    </View>
  );
};

const AddProduct = () => {
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);
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

  const showModal = (type: 'success' | 'error' | 'info', message: string) => {
    setModalType(type);
    setModalMessage(message);
    setIsModalVisible(true);
  };

  return (
    <KeyboardAvoidingView
      style={{ flex: 1 }}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <Text style={styles.title}>Add Product</Text>
        <Text style={styles.subtitle}>Fill in the details to list your product</Text>

        {/* Image Upload */}
        <Text style={styles.sectionTitle}>
          Photos <Text style={styles.imageCountInline}>({productImages.length}/10)</Text>
        </Text>
        {errors.images && <Text style={styles.errorText}>{errors.images}</Text>}

        <View style={styles.imageUploadRow}>
          <TouchableOpacity style={styles.imageUploadArea} onPress={pickProductImages} activeOpacity={0.7}>
            <Icon name="photo-library" size={28} color={GOLD.dark} />
            <Text style={styles.imageUploadText}>Gallery</Text>
          </TouchableOpacity>
          <TouchableOpacity style={styles.imageUploadArea} onPress={takePhoto} activeOpacity={0.7}>
            <Icon name="camera-alt" size={28} color={GOLD.dark} />
            <Text style={styles.imageUploadText}>Camera</Text>
          </TouchableOpacity>
        </View>

        {productImages.length > 0 && (
          <ScrollView horizontal showsHorizontalScrollIndicator={false} style={styles.imagePreviewRow}>
            {productImages.map((uri, index) => (
              <View key={index} style={styles.imagePreviewWrapper}>
                <Image source={{ uri }} style={styles.imagePreview} />
                <TouchableOpacity style={styles.imageRemoveBtn} onPress={() => removeImage(index)}>
                  <Icon name="close" size={16} color="#fff" />
                </TouchableOpacity>
              </View>
            ))}
          </ScrollView>
        )}

        {/* Product Details */}
        <Text style={styles.sectionTitle}>Product Details</Text>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Product Name *</Text>
          <TextInput
            style={[styles.input, errors.productName && styles.inputError]}
            value={productName}
            onChangeText={setProductName}
            placeholder="e.g. Organic Honey Jar"
            placeholderTextColor="#bbb"
            returnKeyType="next"
            onSubmitEditing={() => descriptionRef.current?.focus()}
          />
          {errors.productName && <Text style={styles.errorText}>{errors.productName}</Text>}
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Category *</Text>
          {categoriesLoading ? (
            <View style={styles.categoryLoadingRow}>
              <ActivityIndicator size="small" color={GOLD.dark} />
              <Text style={styles.categoryLoadingText}>Loading categories...</Text>
            </View>
          ) : categories.length === 0 ? (
            <TouchableOpacity style={styles.categoryLoadingRow} onPress={fetchCategories}>
              <Text style={styles.categoryLoadingText}>No categories found. Tap to retry.</Text>
            </TouchableOpacity>
          ) : (
            <TouchableOpacity
              activeOpacity={0.7}
              style={[
                styles.categorySelector,
                errors.category && styles.inputError,
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
              <Icon name="keyboard-arrow-down" size={24} color="#bbb" />
            </TouchableOpacity>
          )}
          {errors.category && <Text style={styles.errorText}>{errors.category}</Text>}
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Description *</Text>
          <TextInput
            ref={descriptionRef}
            style={[styles.textArea, errors.description && styles.inputError]}
            placeholder="Describe your product, quality, and what buyers can expect..."
            placeholderTextColor="#bbb"
            numberOfLines={5}
            multiline
            maxLength={maxDescriptionLength}
            value={description}
            onChangeText={setDescription}
            textAlignVertical="top"
          />
          <Text style={styles.charCount}>
            {description.length}/{maxDescriptionLength}
          </Text>
          {errors.description && <Text style={styles.errorText}>{errors.description}</Text>}
        </View>

        <View style={styles.rowFields}>
          <View style={styles.halfField}>
            <Text style={styles.label}>Price ($) *</Text>
            <TextInput
              ref={priceRef}
              style={[styles.input, errors.price && styles.inputError]}
              placeholder="0.00"
              placeholderTextColor="#bbb"
              keyboardType="decimal-pad"
              value={price}
              onChangeText={setPrice}
            />
            {errors.price && <Text style={styles.errorText}>{errors.price}</Text>}
            <Text style={styles.feeHint}>2% platform fee applies per sale</Text>
          </View>
          <View style={styles.halfField}>
            <Text style={styles.label}>Stock *</Text>
            <TextInput
              ref={stockRef}
              style={[styles.input, errors.stock && styles.inputError]}
              placeholder="0"
              placeholderTextColor="#bbb"
              keyboardType="number-pad"
              value={stock}
              onChangeText={setStock}
            />
            {errors.stock && <Text style={styles.errorText}>{errors.stock}</Text>}
          </View>
        </View>

        {/* Shipping Rates */}
        <Text style={styles.sectionTitle}>Shipping Rates ($)</Text>

        <Text style={[styles.label, { marginBottom: 6 }]}>Package size (optional quick-select)</Text>
        <View style={styles.presetRow}>
          {PACKAGE_SIZE_PRESETS.map((preset) => (
            <Pressable
              key={preset.key}
              style={styles.presetChip}
              onPress={() => applyPackagePreset(preset.baseFee)}
            >
              <Text style={styles.presetLabel}>{preset.label}</Text>
              <Text style={styles.presetDesc}>${preset.baseFee}</Text>
            </Pressable>
          ))}
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Base shipping fee ($)</Text>
          <TextInput
            style={styles.input}
            placeholder="e.g. 2.00"
            placeholderTextColor="#bbb"
            keyboardType="decimal-pad"
            value={baseShippingFee}
            onChangeText={applyBaseShippingFee}
          />
          <Text style={styles.helperText}>Auto-fills the zone rates below. Edit any zone to override.</Text>
        </View>

        <View style={styles.rowFields}>
          <View style={styles.halfField}>
            <Text style={styles.label}>Same Residential Area</Text>
            <TextInput
              style={[styles.input, { backgroundColor: '#f0f0f0', color: '#888' }]}
              value="Free"
              editable={false}
            />
          </View>
          <View style={styles.halfField}>
            <Text style={styles.label}>Same City</Text>
            <TextInput
              style={styles.input}
              placeholder="0.00"
              placeholderTextColor="#bbb"
              keyboardType="decimal-pad"
              value={shippingSameCity}
              onChangeText={setShippingSameCity}
            />
          </View>
        </View>
        <View style={styles.rowFields}>
          <View style={styles.halfField}>
            <Text style={styles.label}>Same State</Text>
            <TextInput
              style={styles.input}
              placeholder="0.00"
              placeholderTextColor="#bbb"
              keyboardType="decimal-pad"
              value={shippingSameState}
              onChangeText={setShippingSameState}
            />
          </View>
          <View style={styles.halfField}>
            <Text style={styles.label}>Same Country</Text>
            <TextInput
              style={styles.input}
              placeholder="0.00"
              placeholderTextColor="#bbb"
              keyboardType="decimal-pad"
              value={shippingSameCountry}
              onChangeText={setShippingSameCountry}
            />
          </View>
        </View>
        <View style={styles.rowFields}>
          <View style={styles.halfField}>
            <Text style={styles.label}>International</Text>
            <TextInput
              style={styles.input}
              placeholder="0.00"
              placeholderTextColor="#bbb"
              keyboardType="decimal-pad"
              value={shippingInternational}
              onChangeText={setShippingInternational}
            />
          </View>
        </View>

        {/* Contact & Location */}
        <Text style={styles.sectionTitle}>Contact & Location</Text>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Company Name</Text>
          <TextInput
            ref={companyRef}
            style={styles.input}
            value={companyName}
            onChangeText={setCompanyName}
            placeholder="Your business name"
            placeholderTextColor="#bbb"
          />
        </View>

        <View style={styles.inputGroup}>
          <LocationPicker
            value={location}
            onChange={setLocation}
            required={["country", "state"]}
          />
          {errors.location && <Text style={styles.errorText}>{errors.location}</Text>}
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Phone Number *</Text>
          <View style={styles.inputWithIcon}>
            <Icon name="phone" size={20} color={GOLD.dark} style={{ marginRight: 8 }} />
            <TextInput
              ref={phoneRef}
              style={{ flex: 1, fontSize: 15, color: '#000' }}
              value={phoneNumber}
              onChangeText={setPhoneNumber}
              keyboardType="phone-pad"
              placeholder="+234 800 000 0000"
              placeholderTextColor="#bbb"
              returnKeyType="next"
              onSubmitEditing={() => emailRef.current?.focus()}
            />
          </View>
          {errors.phoneNumber && <Text style={styles.errorText}>{errors.phoneNumber}</Text>}
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Email *</Text>
          <View style={styles.inputWithIcon}>
            <Icon name="email" size={20} color={GOLD.dark} style={{ marginRight: 8 }} />
            <TextInput
              ref={emailRef}
              style={{ flex: 1, fontSize: 15, color: '#000' }}
              value={email}
              onChangeText={setEmail}
              keyboardType="email-address"
              autoCapitalize="none"
              placeholder="you@example.com"
              placeholderTextColor="#bbb"
            />
          </View>
          {errors.email && <Text style={styles.errorText}>{errors.email}</Text>}
        </View>

        {/* Attributes */}
        <View style={styles.sectionHeader}>
          <Text style={styles.sectionTitle}>Attributes</Text>
          <TouchableOpacity style={styles.addAttrBtn} onPress={handleAddAttribute}>
            <Icon name="add" size={20} color={GOLD.dark} />
            <Text style={styles.addAttrBtnText}>Add</Text>
          </TouchableOpacity>
        </View>

        {attributes.length > 0 ? (
          attributes.map((item, index) => (
            <View key={index} style={styles.attributeRow}>
              <TextInput
                style={[styles.attributeInput, errors[`attr_name_${index}`] && styles.inputError]}
                placeholder="e.g. Size"
                placeholderTextColor="#bbb"
                value={item.name}
                onChangeText={(value) => handleAttributeChange(index, 'name', value)}
              />
              <TextInput
                style={[styles.attributeInput, errors[`attr_value_${index}`] && styles.inputError]}
                placeholder="e.g. Large"
                placeholderTextColor="#bbb"
                value={item.value}
                onChangeText={(value) => handleAttributeChange(index, 'value', value)}
              />
              <TouchableOpacity onPress={() => handleRemoveAttribute(index)} style={{ padding: 6 }}>
                <Icon name="delete-outline" size={22} color="#DC143C" />
              </TouchableOpacity>
            </View>
          ))
        ) : (
          <Text style={styles.hintText}>Add attributes like size, color, material, etc.</Text>
        )}

        {/* Submit */}
        {loading && (
          <View style={{ marginTop: 16 }}>
            <ActivityIndicator size="small" color={GOLD.dark} />
            <Text style={{ textAlign: 'center', marginTop: 8, color: '#999' }}>Submitting...</Text>
          </View>
        )}

        <TouchableOpacity
          style={[styles.submitBtn, loading && { opacity: 0.5 }]}
          onPress={createProduct}
          disabled={loading}
          activeOpacity={0.8}
        >
          <Icon name="add-shopping-cart" size={22} color="#000" />
          <Text style={styles.submitBtnText}>Publish Product</Text>
        </TouchableOpacity>

        <Text style={styles.hintText}>Fields marked with * are required</Text>

        <View style={{ height: 40 }} />
      </ScrollView>

      {/* Category Selection Modal */}
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
                <Icon name="close" size={24} color="#999" />
              </TouchableOpacity>
            </View>
            <View style={styles.categorySearchContainer}>
              <Icon name="search" size={20} color="#999" />
              <TextInput
                style={styles.categorySearchInput}
                placeholder="Search categories..."
                placeholderTextColor="#bbb"
                value={categorySearch}
                onChangeText={setCategorySearch}
                autoCapitalize="none"
              />
              {categorySearch.length > 0 && (
                <TouchableOpacity onPress={() => setCategorySearch('')}>
                  <Icon name="close" size={18} color="#999" />
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
                      {isSelected && <Icon name="check-circle" size={22} color={GOLD.dark} />}
                    </TouchableOpacity>
                  );
                })
              }
              {categories.filter(item => !categorySearch || item.title.toLowerCase().includes(categorySearch.toLowerCase())).length === 0 && (
                <View style={styles.pickerEmpty}>
                  <Icon name="search-off" size={32} color="#bbb" />
                  <Text style={styles.pickerEmptyText}>No categories match "{categorySearch}"</Text>
                </View>
              )}
            </ScrollView>
          </View>
        </View>
      </Modal>

      {/* Success/Error Modal */}
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
              modalType === 'success' && { backgroundColor: '#228B22' },
              modalType === 'error' && { backgroundColor: '#DC143C' },
              modalType === 'info' && { backgroundColor: '#FF8C00' },
            ]}>
              <Icon
                name={modalType === 'success' ? 'check-circle' : modalType === 'error' ? 'error' : 'info'}
                size={48}
                color="#fff"
              />
            </View>
            <Text style={styles.modalTitle}>
              {modalType === 'success' ? 'Success!' : modalType === 'error' ? 'Error!' : 'Information'}
            </Text>
            <Text style={styles.modalMessage}>{modalMessage}</Text>
            <TouchableOpacity style={styles.modalButton} onPress={() => setIsModalVisible(false)} activeOpacity={0.8}>
              <Text style={styles.modalButtonText}>
                {modalType === 'success' ? 'Continue' : 'Try Again'}
              </Text>
            </TouchableOpacity>
          </View>
        </View>
      </Modal>
    </KeyboardAvoidingView>
  );
};

export default AddProduct;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: GOLD.bg,
    paddingHorizontal: 20,
  },
  title: {
    fontSize: 26,
    fontWeight: '800',
    color: '#000',
    marginTop: 20,
  },
  subtitle: {
    fontSize: 14,
    color: '#999',
    marginTop: 4,
    marginBottom: 24,
  },
  sectionTitle: {
    fontSize: 17,
    fontWeight: '700',
    color: '#000',
    marginTop: 24,
    marginBottom: 12,
  },
  sectionHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    marginTop: 24,
    marginBottom: 12,
  },
  imageCountInline: {
    fontSize: 14,
    fontWeight: '500',
    color: '#999',
  },
  inputGroup: {
    marginBottom: 16,
  },
  label: {
    fontSize: 13,
    fontWeight: '600',
    color: '#555',
    marginBottom: 6,
  },
  input: {
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#E8E8E8',
    borderRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: 13,
    fontSize: 15,
    color: '#000',
  },
  inputError: {
    borderColor: '#DC143C',
    backgroundColor: '#DC143C08',
  },
  errorText: {
    fontSize: 12,
    color: '#DC143C',
    marginTop: 4,
    marginLeft: 4,
  },
  feeHint: {
    fontSize: 11,
    color: '#888',
    marginTop: 4,
    marginLeft: 4,
  },
  inputWithIcon: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#E8E8E8',
    borderRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: Platform.OS === 'ios' ? 13 : 4,
  },
  textArea: {
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#E8E8E8',
    borderRadius: 12,
    padding: 14,
    fontSize: 15,
    color: '#000',
    minHeight: 120,
  },
  charCount: {
    textAlign: 'right',
    color: '#bbb',
    fontSize: 12,
    marginTop: 4,
  },
  rowFields: {
    flexDirection: 'row',
    gap: 12,
  },
  halfField: {
    flex: 1,
    marginBottom: 16,
  },

  // Shipping presets
  presetRow: {
    flexDirection: 'row',
    gap: 8,
    marginBottom: 14,
    flexWrap: 'wrap',
  },
  presetChip: {
    flex: 1,
    minWidth: 90,
    paddingVertical: 10,
    paddingHorizontal: 10,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#E0E0E0',
    backgroundColor: '#FAFAFA',
    alignItems: 'center',
  },
  presetLabel: {
    fontSize: 12,
    fontWeight: '700',
    color: '#1A1A1A',
    textAlign: 'center',
  },
  presetDesc: {
    fontSize: 11,
    color: GOLD.dark,
    marginTop: 2,
    fontWeight: '600',
  },
  helperText: {
    fontSize: 11,
    color: '#888',
    marginTop: 4,
    fontStyle: 'italic',
  },

  // Image upload
  imageUploadRow: {
    flexDirection: 'row',
    gap: 12,
  },
  imageUploadArea: {
    flex: 1,
    backgroundColor: '#fff',
    borderWidth: 2,
    borderColor: GOLD.muted,
    borderStyle: 'dashed',
    borderRadius: 16,
    paddingVertical: 24,
    alignItems: 'center',
    justifyContent: 'center',
  },
  imageUploadText: {
    fontSize: 14,
    fontWeight: '600',
    color: GOLD.dark,
    marginTop: 6,
  },
  imagePreviewRow: {
    marginTop: 12,
    marginBottom: 4,
  },
  imagePreviewWrapper: {
    marginRight: 10,
    position: 'relative',
  },
  imagePreview: {
    width: 80,
    height: 80,
    borderRadius: 12,
    backgroundColor: '#eee',
  },
  imageRemoveBtn: {
    position: 'absolute',
    top: -6,
    right: -6,
    backgroundColor: 'rgba(0,0,0,0.6)',
    borderRadius: 12,
    width: 24,
    height: 24,
    alignItems: 'center',
    justifyContent: 'center',
  },

  // Category selector
  categorySelector: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    backgroundColor: '#fff',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: '#E8E8E8',
    paddingHorizontal: 14,
    paddingVertical: 13,
  },
  categorySelectorSelected: {
    borderColor: GOLD.dark + '60',
    backgroundColor: GOLD.light,
  },
  categorySelectorValue: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 10,
    flex: 1,
  },
  categorySelectorText: {
    fontSize: 15,
    fontWeight: '600',
    color: '#000',
  },
  categorySelectorPlaceholder: {
    fontSize: 15,
    color: '#bbb',
  },
  categoryLoadingRow: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingVertical: 12,
  },
  categoryLoadingText: {
    fontSize: 14,
    color: '#999',
    marginLeft: 8,
  },

  // Attributes
  addAttrBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 4,
    backgroundColor: GOLD.light,
    paddingHorizontal: 12,
    paddingVertical: 6,
    borderRadius: 8,
  },
  addAttrBtnText: {
    fontSize: 13,
    fontWeight: '600',
    color: GOLD.dark,
  },
  attributeRow: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 8,
    marginBottom: 10,
  },
  attributeInput: {
    flex: 1,
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#E8E8E8',
    borderRadius: 12,
    paddingHorizontal: 14,
    paddingVertical: 10,
    fontSize: 14,
    color: '#000',
  },
  hintText: {
    fontSize: 13,
    color: '#bbb',
    textAlign: 'center',
    paddingVertical: 8,
  },

  // Submit
  submitBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD.primary,
    borderRadius: 14,
    paddingVertical: 16,
    marginTop: 24,
    gap: 8,
  },
  submitBtnText: {
    fontSize: 17,
    fontWeight: '700',
    color: '#000',
  },

  // Category modal
  pickerOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.4)',
    justifyContent: 'flex-end',
  },
  pickerOverlayDismiss: {
    flex: 1,
  },
  pickerModal: {
    backgroundColor: '#fff',
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
    borderBottomColor: '#E8E8E8',
  },
  pickerModalTitle: {
    fontSize: 18,
    fontWeight: '700',
    color: '#000',
  },
  categorySearchContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#F5F5F5',
    marginHorizontal: 16,
    marginVertical: 12,
    borderRadius: 10,
    paddingHorizontal: 12,
    paddingVertical: Platform.OS === 'ios' ? 10 : 4,
    gap: 8,
  },
  categorySearchInput: {
    flex: 1,
    fontSize: 15,
    color: '#000',
  },
  categoryListItem: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
  },
  categoryListItemSelected: {
    backgroundColor: GOLD.light,
  },
  categoryItemImage: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: '#eee',
  },
  categoryItemAvatar: {
    width: 40,
    height: 40,
    borderRadius: 20,
    justifyContent: 'center',
    alignItems: 'center',
  },
  categoryItemAvatarText: {
    fontSize: 18,
    fontWeight: '700',
    color: '#fff',
  },
  categoryListItemInfo: {
    flex: 1,
    marginLeft: 12,
  },
  categoryListItemTitle: {
    fontSize: 15,
    fontWeight: '500',
    color: '#333',
  },
  categoryListItemTitleSelected: {
    color: GOLD.dark,
    fontWeight: '600',
  },
  categoryListItemDesc: {
    fontSize: 12,
    color: '#bbb',
    marginTop: 2,
  },
  pickerEmpty: {
    padding: 40,
    alignItems: 'center',
  },
  pickerEmptyText: {
    marginTop: 12,
    fontSize: 14,
    color: '#999',
  },

  // Result modal
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0,0,0,0.5)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContainer: {
    backgroundColor: '#fff',
    borderRadius: 20,
    padding: 28,
    width: '100%',
    maxWidth: 400,
    alignItems: 'center',
  },
  modalIconContainer: {
    width: 72,
    height: 72,
    borderRadius: 36,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  modalTitle: {
    fontSize: 22,
    fontWeight: '700',
    color: '#000',
    marginBottom: 8,
  },
  modalMessage: {
    fontSize: 15,
    color: '#333',
    textAlign: 'center',
    marginBottom: 20,
    lineHeight: 22,
  },
  modalButton: {
    backgroundColor: GOLD.primary,
    borderRadius: 12,
    paddingHorizontal: 28,
    paddingVertical: 12,
    width: '100%',
  },
  modalButtonText: {
    fontSize: 15,
    fontWeight: '600',
    color: '#000',
    textAlign: 'center',
  },
});
