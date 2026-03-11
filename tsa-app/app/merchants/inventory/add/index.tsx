import React, { useState, useEffect } from 'react';
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
  FlatList,
} from 'react-native';
import { router } from 'expo-router';
import Icon from 'react-native-vector-icons/MaterialIcons';
import * as ImagePicker from 'expo-image-picker';
import { LinearGradient } from 'expo-linear-gradient';
//import { api } from '../../services/api';
import { useAuth } from '@/AuthContext/AuthContext';
import api from '@/components/services/api';

const { width, height } = Dimensions.get('window');

// Color palette with brown as primary
const COLORS = {
  primary: '#8B4513', // Saddle Brown
  primaryLight: '#A0522D', // Sienna
  primaryDark: '#654321', // Dark Brown
  secondary: '#D2691E', // Chocolate
  accent: '#DEB887', // Burlywood
  background: '#F5F5DC', // Beige
  surface: '#FFF8DC', // Cornsilk
  text: '#333333',
  textLight: '#666666',
  textLighter: '#999999',
  border: '#D2B48C', // Tan
  success: '#228B22', // Forest Green
  error: '#DC143C', // Crimson
  warning: '#FF8C00', // Dark Orange
  white: '#FFFFFF',
  gray: '#E8E8E8',
  darkGray: '#A9A9A9',
};

const AddProduct = () => {
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);
  const [isModalVisible, setIsModalVisible] = useState(false);
  const [modalMessage, setModalMessage] = useState('');
  const [modalType, setModalType] = useState('success'); // 'success' | 'error' | 'info'

  // Form states
  const [productName, setProductName] = useState('');
  const [description, setDescription] = useState('');
  const [price, setPrice] = useState('');
  const [stock, setStock] = useState('0');
  const [category, setCategory] = useState('');
  const [location, setLocation] = useState('');
  const [phoneNumber, setPhoneNumber] = useState('');
  const [email, setEmail] = useState('');
  const [companyName, setCompanyName] = useState('');
  const [productImages, setProductImages] = useState<string[]>([]);
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const [categories, setCategories] = useState<any[]>([]);

  // Form validation
  const [errors, setErrors] = useState<Record<string, string>>({});

  // Max description length
  const maxDescriptionLength = 500;

  // Fetch categories on mount
  useEffect(() => {
    fetchCategories();
  }, []);

  const fetchCategories = async () => {
    try {
      const response = await api.getCategories({ type: 'Product', active: true });
      if (response.success && response.data) {
        setCategories(response.data);
      }
    } catch (error) {
      showModal('error', 'Failed to load categories');
    }
  };

  // Image picker
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

  // Remove image
  const removeImage = (index: number) => {
    setProductImages(prev => prev.filter((_, i) => i !== index));
  };

  // Take photo
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

  // Attribute management
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

  // Form validation
  const validateForm = () => {
    const newErrors: Record<string, string> = {};

    if (!productName.trim()) newErrors.productName = 'Product name is required';
    if (!description.trim()) newErrors.description = 'Description is required';
    if (description.length > maxDescriptionLength) newErrors.description = `Description must be less than ${maxDescriptionLength} characters`;
    if (!price.trim()) newErrors.price = 'Price is required';
    if (isNaN(parseFloat(price)) || parseFloat(price) <= 0) newErrors.price = 'Price must be a positive number';
    if (!stock.trim()) newErrors.stock = 'Stock is required';
    if (isNaN(parseInt(stock)) || parseInt(stock) < 0) newErrors.stock = 'Stock must be a non-negative number';
    if (!category) newErrors.category = 'Category is required';
    if (!location.trim()) newErrors.location = 'Location is required';
    if (!phoneNumber.trim()) newErrors.phoneNumber = 'Phone number is required';
    if (!email.trim()) newErrors.email = 'Email is required';
    if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email)) newErrors.email = 'Invalid email address';
    if (productImages.length === 0) newErrors.images = 'At least one image is required';

    // Validate attributes
    attributes.forEach((attr, index) => {
      if (!attr.name.trim()) newErrors[`attr_name_${index}`] = 'Attribute name is required';
      if (!attr.value.trim()) newErrors[`attr_value_${index}`] = 'Attribute value is required';
    });

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  // Create product
  const createProduct = async () => {
    if (!validateForm()) {
      showModal('error', 'Please fix all errors before submitting');
      return;
    }

    setLoading(true);

    try {
      // Create FormData
      const formData = new FormData();

      // Add images
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

      // Add other fields
      formData.append('name', productName);
      formData.append('description', description);
      formData.append('price', parseFloat(price).toString());
      formData.append('stock', parseInt(stock).toString());
      formData.append('category', category);
      formData.append('location', location);
      formData.append('phoneNumber', phoneNumber);
      formData.append('email', email);
      if (companyName.trim()) formData.append('companyName', companyName);
      if (attributes.length > 0) {
        formData.append('attributes', JSON.stringify(attributes));
      }

      // Call API
      const response = await api.createProduct(formData);

      if (response.success) {
        showModal('success', 'Product created successfully!');
        resetForm();
        // Navigate back after delay
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

  // Reset form
  const resetForm = () => {
    setProductName('');
    setDescription('');
    setPrice('');
    setStock('0');
    setCategory('');
    setLocation('');
    setPhoneNumber('');
    setEmail('');
    setCompanyName('');
    setProductImages([]);
    setAttributes([]);
    setErrors({});
  };

  // Show modal
  const showModal = (type: 'success' | 'error' | 'info', message: string) => {
    setModalType(type);
    setModalMessage(message);
    setIsModalVisible(true);
  };

  // Render attribute item
  const renderAttributeItem = ({ item, index }: { item: { name: string; value: string }; index: number }) => (
    <View style={styles.attributeItem}>
      <View style={styles.attributeInputs}>
        <TextInput
          style={[styles.attributeInput, errors[`attr_name_${index}`] && styles.inputError]}
          placeholder="Attribute Name"
          placeholderTextColor={COLORS.textLighter}
          value={item.name}
          onChangeText={(value) => handleAttributeChange(index, 'name', value)}
        />
        <TextInput
          style={[styles.attributeInput, errors[`attr_value_${index}`] && styles.inputError]}
          placeholder="Value"
          placeholderTextColor={COLORS.textLighter}
          value={item.value}
          onChangeText={(value) => handleAttributeChange(index, 'value', value)}
        />
      </View>
      <Pressable
        style={styles.removeAttributeButton}
        onPress={() => handleRemoveAttribute(index)}
      >
        <Icon name="delete" size={24} color={COLORS.error} />
      </Pressable>
    </View>
  );

  // Render image item
  const renderImageItem = ({ item, index }: { item: string; index: number }) => (
    <View style={styles.imageItem}>
      <Image source={{ uri: item }} style={styles.productImage} />
      <Pressable
        style={styles.removeImageButton}
        onPress={() => removeImage(index)}
      >
        <Icon name="close" size={20} color={COLORS.white} />
      </Pressable>
    </View>
  );

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : 'height'}
      style={styles.container}
    >
      <LinearGradient
        colors={[COLORS.background, COLORS.surface]}
        style={styles.gradientBackground}
      >
        <ScrollView
          style={styles.scrollView}
          showsVerticalScrollIndicator={false}
          contentContainerStyle={styles.scrollContent}
        >
          {/* Header */}
          <View style={styles.header}>
            <Pressable
              style={styles.backButton}
              onPress={() => router.back()}
            >
              <Icon name="arrow-back" size={28} color={COLORS.primary} />
            </Pressable>
            <Text style={styles.headerTitle}>Add New Product</Text>
            <Pressable
              style={styles.resetButton}
              onPress={resetForm}
              disabled={loading}
            >
              <Icon name="refresh" size={24} color={COLORS.primary} />
            </Pressable>
          </View>

          {/* Main Form */}
          <View style={styles.formContainer}>
            {/* Basic Information Card */}
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
                />
                {errors.productName && (
                  <Text style={styles.errorText}>{errors.productName}</Text>
                )}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Description *</Text>
                <View style={[styles.textAreaContainer, errors.description && styles.inputError]}>
                  <TextInput
                    style={styles.textArea}
                    placeholder="Describe your product..."
                    placeholderTextColor={COLORS.textLighter}
                    multiline
                    numberOfLines={4}
                    value={description}
                    onChangeText={setDescription}
                    maxLength={maxDescriptionLength}
                  />
                  <Text style={styles.charCount}>
                    {description.length}/{maxDescriptionLength}
                  </Text>
                </View>
                {errors.description && (
                  <Text style={styles.errorText}>{errors.description}</Text>
                )}
              </View>

              <View style={styles.row}>
                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Price ($) *</Text>
                  <TextInput
                    style={[styles.input, errors.price && styles.inputError]}
                    placeholder="0.00"
                    placeholderTextColor={COLORS.textLighter}
                    keyboardType="decimal-pad"
                    value={price}
                    onChangeText={setPrice}
                  />
                  {errors.price && (
                    <Text style={styles.errorText}>{errors.price}</Text>
                  )}
                </View>

                <View style={[styles.formGroup, styles.halfWidth]}>
                  <Text style={styles.label}>Stock *</Text>
                  <TextInput
                    style={[styles.input, errors.stock && styles.inputError]}
                    placeholder="0"
                    placeholderTextColor={COLORS.textLighter}
                    keyboardType="number-pad"
                    value={stock}
                    onChangeText={setStock}
                  />
                  {errors.stock && (
                    <Text style={styles.errorText}>{errors.stock}</Text>
                  )}
                </View>
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Category *</Text>
                <View style={[styles.pickerContainer, errors.category && styles.inputError]}>
                  <Icon name="category" size={20} color={COLORS.primary} style={styles.pickerIcon} />
                  <View style={styles.pickerWrapper}>
                    {categories.length > 0 ? (
                      <FlatList
                        data={categories}
                        keyExtractor={(item) => item._id}
                        horizontal
                        showsHorizontalScrollIndicator={false}
                        renderItem={({ item }) => (
                          <Pressable
                            style={[
                              styles.categoryChip,
                              category === item._id && styles.categoryChipSelected,
                            ]}
                            onPress={() => setCategory(item._id)}
                          >
                            <Text
                              style={[
                                styles.categoryChipText,
                                category === item._id && styles.categoryChipTextSelected,
                              ]}
                            >
                              {item.title}
                            </Text>
                          </Pressable>
                        )}
                      />
                    ) : (
                      <Text style={styles.noCategoriesText}>No categories available</Text>
                    )}
                  </View>
                </View>
                {errors.category && (
                  <Text style={styles.errorText}>{errors.category}</Text>
                )}
              </View>
            </View>

            {/* Contact Information Card */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="contact-phone" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Contact Information</Text>
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Location *</Text>
                <TextInput
                  style={[styles.input, errors.location && styles.inputError]}
                  placeholder="Enter location"
                  placeholderTextColor={COLORS.textLighter}
                  value={location}
                  onChangeText={setLocation}
                />
                {errors.location && (
                  <Text style={styles.errorText}>{errors.location}</Text>
                )}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Phone Number *</Text>
                <TextInput
                  style={[styles.input, errors.phoneNumber && styles.inputError]}
                  placeholder="Enter phone number"
                  placeholderTextColor={COLORS.textLighter}
                  keyboardType="phone-pad"
                  value={phoneNumber}
                  onChangeText={setPhoneNumber}
                />
                {errors.phoneNumber && (
                  <Text style={styles.errorText}>{errors.phoneNumber}</Text>
                )}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Email *</Text>
                <TextInput
                  style={[styles.input, errors.email && styles.inputError]}
                  placeholder="Enter email address"
                  placeholderTextColor={COLORS.textLighter}
                  keyboardType="email-address"
                  autoCapitalize="none"
                  value={email}
                  onChangeText={setEmail}
                />
                {errors.email && (
                  <Text style={styles.errorText}>{errors.email}</Text>
                )}
              </View>

              <View style={styles.formGroup}>
                <Text style={styles.label}>Company Name (Optional)</Text>
                <TextInput
                  style={styles.input}
                  placeholder="Enter company name"
                  placeholderTextColor={COLORS.textLighter}
                  value={companyName}
                  onChangeText={setCompanyName}
                />
              </View>
            </View>

            {/* Images Card */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="photo-library" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Product Images *</Text>
                <Text style={styles.imageCount}>
                  ({productImages.length}/10)
                </Text>
              </View>

              {errors.images && (
                <Text style={styles.errorText}>{errors.images}</Text>
              )}

              <View style={styles.imageUploadContainer}>
                <Pressable
                  style={styles.imageUploadButton}
                  onPress={pickProductImages}
                >
                  <LinearGradient
                    colors={[COLORS.primaryLight, COLORS.primary]}
                    style={styles.uploadButtonGradient}
                  >
                    <Icon name="photo-library" size={32} color={COLORS.white} />
                    <Text style={styles.uploadButtonText}>Gallery</Text>
                  </LinearGradient>
                </Pressable>

                <Pressable
                  style={styles.imageUploadButton}
                  onPress={takePhoto}
                >
                  <LinearGradient
                    colors={[COLORS.secondary, COLORS.primaryDark]}
                    style={styles.uploadButtonGradient}
                  >
                    <Icon name="camera-alt" size={32} color={COLORS.white} />
                    <Text style={styles.uploadButtonText}>Camera</Text>
                  </LinearGradient>
                </Pressable>
              </View>

              {productImages.length > 0 && (
                <View style={styles.imagesContainer}>
                  <FlatList
                    data={productImages}
                    keyExtractor={(item, index) => index.toString()}
                    horizontal
                    showsHorizontalScrollIndicator={false}
                    renderItem={renderImageItem}
                    contentContainerStyle={styles.imagesList}
                  />
                </View>
              )}
            </View>

            {/* Attributes Card */}
            <View style={styles.card}>
              <View style={styles.cardHeader}>
                <Icon name="tune" size={24} color={COLORS.primary} />
                <Text style={styles.cardTitle}>Product Attributes</Text>
                <Pressable
                  style={styles.addAttributeHeaderButton}
                  onPress={handleAddAttribute}
                >
                  <Icon name="add" size={24} color={COLORS.primary} />
                </Pressable>
              </View>

              {attributes.length > 0 ? (
                <FlatList
                  data={attributes}
                  keyExtractor={(item, index) => index.toString()}
                  renderItem={renderAttributeItem}
                  scrollEnabled={false}
                />
              ) : (
                <View style={styles.noAttributesContainer}>
                  <Icon name="tune" size={48} color={COLORS.border} />
                  <Text style={styles.noAttributesText}>
                    No attributes added yet
                  </Text>
                  <Text style={styles.noAttributesSubtext}>
                    Add attributes like size, color, material, etc.
                  </Text>
                  <Pressable
                    style={styles.addFirstAttributeButton}
                    onPress={handleAddAttribute}
                  >
                    <Text style={styles.addFirstAttributeButtonText}>
                      Add First Attribute
                    </Text>
                  </Pressable>
                </View>
              )}

              {attributes.length > 0 && (
                <Pressable
                  style={styles.addAttributeButton}
                  onPress={handleAddAttribute}
                >
                  <Icon name="add-circle" size={24} color={COLORS.primary} />
                  <Text style={styles.addAttributeButtonText}>
                    Add Another Attribute
                  </Text>
                </Pressable>
              )}
            </View>

            {/* Submit Button */}
            <Pressable
              style={[styles.submitButton, loading && styles.submitButtonDisabled]}
              onPress={createProduct}
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
                    <Icon name="add-shopping-cart" size={24} color={COLORS.white} />
                    <Text style={styles.submitButtonText}>Create Product</Text>
                  </>
                )}
              </LinearGradient>
            </Pressable>

            {/* Form Help */}
            <View style={styles.helpContainer}>
              <Icon name="help" size={18} color={COLORS.textLight} />
              <Text style={styles.helpText}>
                Fields marked with * are required
              </Text>
            </View>
          </View>
        </ScrollView>
      </LinearGradient>

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
              modalType === 'success' && styles.modalIconSuccess,
              modalType === 'error' && styles.modalIconError,
              modalType === 'info' && styles.modalIconInfo,
            ]}>
              <Icon
                name={
                  modalType === 'success' ? 'check-circle' :
                    modalType === 'error' ? 'error' : 'info'
                }
                size={48}
                color={COLORS.white}
              />
            </View>
            <Text style={styles.modalTitle}>
              {modalType === 'success' ? 'Success!' :
                modalType === 'error' ? 'Error!' : 'Information'}
            </Text>
            <Text style={styles.modalMessage}>{modalMessage}</Text>
            <Pressable
              style={styles.modalButton}
              onPress={() => setIsModalVisible(false)}
            >
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
  container: {
    flex: 1,
    backgroundColor: COLORS.background,
  },
  gradientBackground: {
    flex: 1,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    paddingBottom: 30,
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'space-between',
    paddingHorizontal: 20,
    paddingTop: Platform.OS === 'ios' ? 60 : 40,
    paddingBottom: 20,
    backgroundColor: COLORS.white,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.border,
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 3,
  },
  backButton: {
    padding: 8,
    borderRadius: 20,
    backgroundColor: COLORS.accent + '20',
  },
  headerTitle: {
    fontSize: 20,
    fontWeight: '700',
    color: COLORS.primary,
    flex: 1,
    textAlign: 'center',
  },
  resetButton: {
    padding: 8,
    borderRadius: 20,
    backgroundColor: COLORS.accent + '20',
  },
  formContainer: {
    paddingHorizontal: 20,
    paddingTop: 20,
  },
  card: {
    backgroundColor: COLORS.white,
    borderRadius: 16,
    padding: 20,
    marginBottom: 20,
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.1,
    shadowRadius: 12,
    elevation: 5,
    borderWidth: 1,
    borderColor: COLORS.border + '30',
  },
  cardHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 20,
    paddingBottom: 12,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.border + '30',
  },
  cardTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.primary,
    marginLeft: 12,
    flex: 1,
  },
  imageCount: {
    fontSize: 14,
    fontWeight: '500',
    color: COLORS.textLight,
    marginLeft: 8,
  },
  formGroup: {
    marginBottom: 16,
  },
  label: {
    fontSize: 14,
    fontWeight: '600',
    color: COLORS.primaryDark,
    marginBottom: 8,
  },
  input: {
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 14,
    fontSize: 16,
    color: COLORS.text,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
  },
  inputError: {
    borderColor: COLORS.error,
    backgroundColor: COLORS.error + '10',
  },
  errorText: {
    fontSize: 12,
    color: COLORS.error,
    marginTop: 4,
    marginLeft: 4,
  },
  textAreaContainer: {
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
    minHeight: 120,
  },
  textArea: {
    padding: 16,
    fontSize: 16,
    color: COLORS.text,
    textAlignVertical: 'top',
    minHeight: 100,
  },
  charCount: {
    textAlign: 'right',
    fontSize: 12,
    color: COLORS.textLight,
    paddingHorizontal: 16,
    paddingBottom: 12,
  },
  row: {
    flexDirection: 'row',
    justifyContent: 'space-between',
  },
  halfWidth: {
    width: '48%',
  },
  pickerContainer: {
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 8,
    minHeight: 56,
  },
  pickerIcon: {
    marginRight: 12,
  },
  pickerWrapper: {
    flex: 1,
  },
  categoryChip: {
    backgroundColor: COLORS.white,
    borderRadius: 20,
    paddingHorizontal: 16,
    paddingVertical: 8,
    marginRight: 8,
    borderWidth: 1,
    borderColor: COLORS.border,
  },
  categoryChipSelected: {
    backgroundColor: COLORS.primary,
    borderColor: COLORS.primary,
  },
  categoryChipText: {
    fontSize: 14,
    fontWeight: '500',
    color: COLORS.text,
  },
  categoryChipTextSelected: {
    color: COLORS.white,
  },
  noCategoriesText: {
    fontSize: 14,
    color: COLORS.textLight,
    fontStyle: 'italic',
    paddingVertical: 12,
  },
  imageUploadContainer: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginBottom: 20,
  },
  imageUploadButton: {
    width: width * 0.4,
    height: width * 0.4,
    borderRadius: 16,
    overflow: 'hidden',
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 6,
  },
  uploadButtonGradient: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  uploadButtonText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.white,
    marginTop: 12,
  },
  imagesContainer: {
    marginTop: 10,
  },
  imagesList: {
    paddingVertical: 10,
  },
  imageItem: {
    position: 'relative',
    marginRight: 12,
  },
  productImage: {
    width: 120,
    height: 120,
    borderRadius: 12,
    borderWidth: 2,
    borderColor: COLORS.border,
  },
  removeImageButton: {
    position: 'absolute',
    top: -8,
    right: -8,
    backgroundColor: COLORS.error,
    borderRadius: 12,
    width: 24,
    height: 24,
    justifyContent: 'center',
    alignItems: 'center',
    shadowColor: COLORS.error,
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.3,
    shadowRadius: 4,
    elevation: 3,
  },
  attributeItem: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 12,
  },
  attributeInputs: {
    flex: 1,
    flexDirection: 'row',
  },
  attributeInput: {
    flex: 1,
    backgroundColor: COLORS.gray,
    borderRadius: 12,
    paddingHorizontal: 16,
    paddingVertical: 14,
    fontSize: 16,
    color: COLORS.text,
    borderWidth: 1,
    borderColor: COLORS.border + '50',
    marginRight: 12,
  },
  removeAttributeButton: {
    padding: 8,
    borderRadius: 12,
    backgroundColor: COLORS.error + '10',
  },
  addAttributeHeaderButton: {
    padding: 4,
    borderRadius: 12,
    backgroundColor: COLORS.accent + '20',
  },
  noAttributesContainer: {
    alignItems: 'center',
    paddingVertical: 40,
    paddingHorizontal: 20,
  },
  noAttributesText: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.textLight,
    marginTop: 16,
    marginBottom: 8,
  },
  noAttributesSubtext: {
    fontSize: 14,
    color: COLORS.textLighter,
    textAlign: 'center',
    marginBottom: 24,
  },
  addFirstAttributeButton: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    paddingHorizontal: 24,
    paddingVertical: 12,
  },
  addFirstAttributeButtonText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.white,
  },
  addAttributeButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: COLORS.accent + '20',
    borderRadius: 12,
    paddingVertical: 14,
    marginTop: 8,
  },
  addAttributeButtonText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.primary,
    marginLeft: 8,
  },
  submitButton: {
    borderRadius: 16,
    overflow: 'hidden',
    marginTop: 10,
    marginBottom: 20,
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.3,
    shadowRadius: 8,
    elevation: 6,
  },
  submitButtonDisabled: {
    opacity: 0.7,
  },
  submitButtonGradient: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 18,
  },
  submitButtonText: {
    fontSize: 18,
    fontWeight: '700',
    color: COLORS.white,
    marginLeft: 12,
  },
  helpContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginTop: 10,
  },
  helpText: {
    fontSize: 14,
    color: COLORS.textLight,
    marginLeft: 8,
  },
  modalOverlay: {
    flex: 1,
    backgroundColor: 'rgba(0, 0, 0, 0.5)',
    justifyContent: 'center',
    alignItems: 'center',
    paddingHorizontal: 20,
  },
  modalContainer: {
    backgroundColor: COLORS.white,
    borderRadius: 20,
    padding: 30,
    width: '100%',
    maxWidth: 400,
    alignItems: 'center',
    shadowColor: COLORS.primaryDark,
    shadowOffset: { width: 0, height: 10 },
    shadowOpacity: 0.3,
    shadowRadius: 20,
    elevation: 10,
  },
  modalIconContainer: {
    width: 80,
    height: 80,
    borderRadius: 40,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 20,
  },
  modalIconSuccess: {
    backgroundColor: COLORS.success,
  },
  modalIconError: {
    backgroundColor: COLORS.error,
  },
  modalIconInfo: {
    backgroundColor: COLORS.warning,
  },
  modalTitle: {
    fontSize: 24,
    fontWeight: '700',
    color: COLORS.primary,
    marginBottom: 12,
  },
  modalMessage: {
    fontSize: 16,
    color: COLORS.text,
    textAlign: 'center',
    marginBottom: 24,
    lineHeight: 24,
  },
  modalButton: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    paddingHorizontal: 32,
    paddingVertical: 14,
    width: '100%',
  },
  modalButtonText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.white,
    textAlign: 'center',
  },
});

export default AddProduct;