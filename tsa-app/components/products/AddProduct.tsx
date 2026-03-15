import {
  Image,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
  Alert,
  Platform,
  KeyboardAvoidingView,
  ActivityIndicator,
} from "react-native";
import React, { useState, useEffect, useCallback } from "react";
import Icon from "react-native-vector-icons/MaterialIcons";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import { Picker } from "@react-native-picker/picker";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  bg: '#FAF9F6',
};

const AddProduct = () => {
  const { token } = useAuth();
  const [productImages, setProductImages] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [productName, setProductName] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [email, setEmail] = useState("");
  const [location, setLocation] = useState("");
  const [category, setCategory] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [price, setPrice] = useState("");
  const [stock, setStock] = useState("");
  const [attributes, setAttributes] = useState<{ name: string; value: string }[]>([]);
  const { setLoading, loading } = useAuth();
  const [categories, setCategories] = useState<{ id: string; title: string }[]>([]);
  const maxDescriptionLength = 500;

  const delay = useCallback((duration: number) => {
    return new Promise((resolve) => setTimeout(resolve, duration));
  }, []);

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(
          `${baseUrl}/products/category/all?type=Product&active=true`,
          { headers: { Authorization: token } }
        );
        const data = response.data.data;
        setCategories(Array.isArray(data) ? data : []);
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

  const createProduct = async () => {
    if (!productName || !location || !phoneNumber || !email || !category || !description || !price || productImages.length === 0) {
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
    formData.append("location", location);
    formData.append("type", "Product");
    formData.append("email", email);
    formData.append("phoneNumber", phoneNumber);
    formData.append("category", category);
    formData.append("companyName", companyName);
    //@ts-ignore
    formData.append("price", parseInt(price));
    //@ts-ignore
    formData.append("stock", parseInt(stock || "0"));
    attributes.forEach((attribute, index) => {
      formData.append(`attributes[${index}].name`, attribute.name);
      formData.append(`attributes[${index}].value`, attribute.value);
    });
    try {
      const response = await axios.post(`${baseUrl}/products`, formData, {
        headers: {
          Authorization: token,
          "Content-Type": "multipart/form-data",
        },
      });
      if (response) {
        setLoading(false);
        Alert.alert("Success", "Product created successfully!");
        resetForm();
      }
    } catch (error: any) {
      setLoading(false);
      Alert.alert("Error", error.response?.data?.message || "Failed to create product.");
    }
  };

  const resetForm = () => {
    setPhoneNumber("");
    setCategory("");
    setDescription("");
    setEmail("");
    setLocation("");
    setProductImages([]);
    setProductName("");
    setPrice("");
    setStock("");
    setAttributes([]);
    setCompanyName("");
  };

  const handleAddAttribute = () => {
    setAttributes([...attributes, { name: "", value: "" }]);
  };

  const handleRemoveAttribute = (index: number) => {
    setAttributes(prev => prev.filter((_, i) => i !== index));
  };

  const handleAttributeChange = (index: number, field: "name" | "value", value: string) => {
    const updated = [...attributes];
    updated[index][field] = value;
    setAttributes(updated);
  };

  return (
    <KeyboardAvoidingView
      style={{ flex: 1 }}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <Text style={styles.title}>Add Product</Text>
        <Text style={styles.subtitle}>Fill in the details to list your product</Text>

        {/* Image Upload */}
        <Text style={styles.sectionTitle}>Photos</Text>
        <TouchableOpacity style={styles.imageUploadArea} onPress={pickProductImages} activeOpacity={0.7}>
          <Icon name="add-a-photo" size={32} color={GOLD.dark} />
          <Text style={styles.imageUploadText}>Tap to upload images</Text>
          <Text style={styles.imageUploadHint}>High quality photos help sell faster</Text>
        </TouchableOpacity>

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
            style={styles.input}
            value={productName}
            onChangeText={setProductName}
            placeholder="e.g. Organic Honey 500ml"
            placeholderTextColor="#bbb"
          />
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Category *</Text>
          <View style={styles.pickerWrapper}>
            <Picker
              selectedValue={category}
              onValueChange={(val) => setCategory(val)}
              style={styles.picker}
            >
              <Picker.Item label="Select a category" value="" color="#bbb" />
              {categories.map((item) => (
                <Picker.Item key={item.id} label={item.title} value={item.id} />
              ))}
            </Picker>
          </View>
        </View>

        <View style={styles.row}>
          <View style={[styles.inputGroup, { flex: 1, marginRight: 8 }]}>
            <Text style={styles.label}>Price (USDT) *</Text>
            <TextInput
              style={styles.input}
              value={price}
              onChangeText={setPrice}
              keyboardType="numeric"
              placeholder="0.00"
              placeholderTextColor="#bbb"
            />
          </View>
          <View style={[styles.inputGroup, { flex: 1, marginLeft: 8 }]}>
            <Text style={styles.label}>Stock</Text>
            <TextInput
              style={styles.input}
              value={stock}
              onChangeText={setStock}
              keyboardType="numeric"
              placeholder="0"
              placeholderTextColor="#bbb"
            />
          </View>
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Description *</Text>
          <TextInput
            style={styles.textArea}
            placeholder="Describe your product..."
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
        </View>

        {/* Contact & Location */}
        <Text style={styles.sectionTitle}>Contact & Location</Text>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Company Name</Text>
          <TextInput
            style={styles.input}
            value={companyName}
            onChangeText={setCompanyName}
            placeholder="Your business name"
            placeholderTextColor="#bbb"
          />
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Location *</Text>
          <View style={styles.inputWithIcon}>
            <Icon name="location-on" size={20} color={GOLD.dark} style={{ marginRight: 8 }} />
            <TextInput
              style={{ flex: 1, fontSize: 15, color: '#000' }}
              value={location}
              onChangeText={setLocation}
              placeholder="City, State"
              placeholderTextColor="#bbb"
            />
          </View>
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Phone Number *</Text>
          <View style={styles.inputWithIcon}>
            <Icon name="phone" size={20} color={GOLD.dark} style={{ marginRight: 8 }} />
            <TextInput
              style={{ flex: 1, fontSize: 15, color: '#000' }}
              value={phoneNumber}
              onChangeText={setPhoneNumber}
              keyboardType="phone-pad"
              placeholder="+234 800 000 0000"
              placeholderTextColor="#bbb"
            />
          </View>
        </View>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Email *</Text>
          <View style={styles.inputWithIcon}>
            <Icon name="email" size={20} color={GOLD.dark} style={{ marginRight: 8 }} />
            <TextInput
              style={{ flex: 1, fontSize: 15, color: '#000' }}
              value={email}
              onChangeText={setEmail}
              keyboardType="email-address"
              autoCapitalize="none"
              placeholder="you@example.com"
              placeholderTextColor="#bbb"
            />
          </View>
        </View>

        {/* Attributes */}
        <Text style={styles.sectionTitle}>Attributes</Text>
        <Text style={styles.attributeHint}>Add custom details like size, color, weight, etc.</Text>

        {attributes.map((attribute, index) => (
          <View key={index} style={styles.attributeRow}>
            <TextInput
              style={[styles.input, { flex: 1, marginRight: 8 }]}
              placeholder="Name"
              placeholderTextColor="#bbb"
              value={attribute.name}
              onChangeText={(value) => handleAttributeChange(index, "name", value)}
            />
            <TextInput
              style={[styles.input, { flex: 1, marginRight: 8 }]}
              placeholder="Value"
              placeholderTextColor="#bbb"
              value={attribute.value}
              onChangeText={(value) => handleAttributeChange(index, "value", value)}
            />
            <TouchableOpacity onPress={() => handleRemoveAttribute(index)} style={styles.removeAttrBtn}>
              <Icon name="delete-outline" size={22} color="#e53935" />
            </TouchableOpacity>
          </View>
        ))}

        <TouchableOpacity style={styles.addAttrBtn} onPress={handleAddAttribute} activeOpacity={0.7}>
          <Icon name="add" size={20} color={GOLD.dark} />
          <Text style={styles.addAttrText}>Add Attribute</Text>
        </TouchableOpacity>

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
          <Icon name="publish" size={22} color="#000" />
          <Text style={styles.submitBtnText}>Publish Product</Text>
        </TouchableOpacity>

        <View style={{ height: 40 }} />
      </ScrollView>
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
  pickerWrapper: {
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#E8E8E8',
    borderRadius: 12,
    overflow: 'hidden',
  },
  picker: {
    marginHorizontal: 4,
  },
  row: {
    flexDirection: 'row',
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
  imageUploadArea: {
    backgroundColor: '#fff',
    borderWidth: 2,
    borderColor: GOLD.muted,
    borderStyle: 'dashed',
    borderRadius: 16,
    paddingVertical: 32,
    alignItems: 'center',
    justifyContent: 'center',
  },
  imageUploadText: {
    fontSize: 15,
    fontWeight: '600',
    color: GOLD.dark,
    marginTop: 8,
  },
  imageUploadHint: {
    fontSize: 12,
    color: '#bbb',
    marginTop: 4,
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
  attributeHint: {
    fontSize: 13,
    color: '#999',
    marginBottom: 12,
    marginTop: -4,
  },
  attributeRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 10,
  },
  removeAttrBtn: {
    padding: 8,
  },
  addAttrBtn: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    backgroundColor: GOLD.light,
    borderWidth: 1,
    borderColor: GOLD.muted,
    borderRadius: 12,
    paddingVertical: 12,
    marginBottom: 8,
  },
  addAttrText: {
    fontSize: 15,
    fontWeight: '600',
    color: GOLD.dark,
    marginLeft: 6,
  },
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
});
