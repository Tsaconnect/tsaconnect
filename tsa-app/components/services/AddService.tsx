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
import React, { useState, useEffect } from "react";
import Icon from "react-native-vector-icons/MaterialIcons";
import * as ImagePicker from "expo-image-picker";
import { useAuth } from "../../AuthContext/AuthContext";
import { Picker } from "@react-native-picker/picker";
import axios from "axios";
import { baseUrl } from "../../constants/api/apiClient";
import LocationPicker, { LocationValue } from "../common/LocationPicker";

const GOLD = {
  primary: '#FFD700',
  dark: '#B8860B',
  light: '#FFF8DC',
  muted: '#F5DEB3',
  bg: '#FAF9F6',
};

const AddService = () => {
  const { token } = useAuth();
  const [loading, setLoading] = useState(false);
  const [productImages, setProductImages] = useState<string[]>([]);
  const [description, setDescription] = useState("");
  const [productName, setProductName] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [email, setEmail] = useState("");
  const [location, setLocation] = useState<LocationValue>({ country: '', state: '', city: '' });
  const [category, setCategory] = useState("");
  const [companyName, setCompanyName] = useState("");
  const [categories, setCategories] = useState<{ id: string; title: string }[]>([]);
  const maxDescriptionLength = 500;

  useEffect(() => {
    const fetchCategories = async () => {
      try {
        const response = await axios.get(
          `${baseUrl}/products/category/all?type=Service&active=true`,
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
    //@ts-ignore
    formData.append("price", 0);
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
  };

  return (
    <KeyboardAvoidingView
      style={{ flex: 1 }}
      behavior={Platform.OS === "ios" ? "padding" : undefined}
    >
      <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
        {/* Header */}
        <Text style={styles.title}>Add Service</Text>
        <Text style={styles.subtitle}>Fill in the details to list your service</Text>

        {/* Image Upload */}
        <Text style={styles.sectionTitle}>Photos</Text>
        <TouchableOpacity style={styles.imageUploadArea} onPress={pickProductImages} activeOpacity={0.7}>
          <Icon name="add-a-photo" size={32} color={GOLD.dark} />
          <Text style={styles.imageUploadText}>Tap to upload images</Text>
          <Text style={styles.imageUploadHint}>Showcase your service with quality photos</Text>
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

        {/* Service Details */}
        <Text style={styles.sectionTitle}>Service Details</Text>

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Service Name *</Text>
          <TextInput
            style={styles.input}
            value={productName}
            onChangeText={setProductName}
            placeholder="e.g. Plumbing & Pipe Repair"
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

        <View style={styles.inputGroup}>
          <Text style={styles.label}>Description *</Text>
          <TextInput
            style={styles.textArea}
            placeholder="Describe your service, expertise, and what clients can expect..."
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
          <LocationPicker
            value={location}
            onChange={setLocation}
            required={["country", "state"]}
          />
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

        {/* Submit */}
        {loading && (
          <View style={{ marginTop: 16 }}>
            <ActivityIndicator size="small" color={GOLD.dark} />
            <Text style={{ textAlign: 'center', marginTop: 8, color: '#999' }}>Submitting...</Text>
          </View>
        )}

        <TouchableOpacity
          style={[styles.submitBtn, loading && { opacity: 0.5 }]}
          onPress={createService}
          disabled={loading}
          activeOpacity={0.8}
        >
          <Icon name="publish" size={22} color="#000" />
          <Text style={styles.submitBtnText}>Publish Service</Text>
        </TouchableOpacity>

        <View style={{ height: 40 }} />
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default AddService;

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
