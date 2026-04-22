import React, { useState } from 'react';
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  TextInput,
  Alert,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
  Image,
  ActivityIndicator,
} from 'react-native';
import { useRouter } from 'expo-router';
import { COLORS } from '../../constants';
import PhoneInput from 'react-native-international-phone-number';
import { Ionicons } from '@expo/vector-icons';
import { useAuth } from '../../AuthContext/AuthContext';
import * as ImagePicker from 'expo-image-picker';
import apiService from '../services/api';

const COUNTRY_TO_CCA2: Record<string, string> = {
  'Nigeria': 'NG',
  'United States': 'US',
  'United Kingdom': 'GB',
  'Ghana': 'GH',
  'South Africa': 'ZA',
  'Kenya': 'KE',
  'Canada': 'CA',
  'India': 'IN',
  'Germany': 'DE',
  'France': 'FR',
};

//@ts-ignore
const EditProfileScreen = ({ user }) => {
  const { token, setCurrentUser } = useAuth();
  const router = useRouter();
  const defaultCca2 = COUNTRY_TO_CCA2[user.country] || 'NG';

  const [name, setName] = useState(user.name || '');
  const [username, setUsername] = useState(user.username || '');
  const [phoneNumber, setPhoneNumber] = useState(user.phoneNumber || '');
  const [address, setAddress] = useState(user.address || '');
  const [selectedCountry, setSelectedCountry] = useState<any>(null);
  const [profileImage, setProfileImage] = useState(
    user.profilePicture || user.profilePhoto?.url || user.profilePhoto
  );
  const [imageUri, setImageUri] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);

  const pickImageAsync = async () => {
    try {
      const { status } =
        await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== 'granted') {
        alert('Permission to access media library denied');
        return;
      }

      let result = await ImagePicker.launchImageLibraryAsync({
        allowsEditing: false,
        aspect: [4, 3],
        quality: 1,
      });

      if (!result.canceled) {
        setProfileImage(result.assets[0].uri);
        //@ts-ignore
        setImageUri(result.assets[0].uri);
      } else {
        alert('You did not select any image.');
      }
    } catch (error) {
      console.error('Error picking image:', error);
    }
  };

  const handleUpdate = async () => {
    if (!name.trim()) {
      Alert.alert('Error', 'Name is required');
      return;
    }

    setSaving(true);

    try {
      if (token) {
        // AuthContext stores the token with the "Bearer " prefix baked in, but
        // apiService.getHeaders() re-adds "Bearer " on every request. Strip it here
        // so we don't end up sending "Authorization: Bearer Bearer <jwt>".
        const rawJwt = token.replace(/^Bearer\s+/i, '');
        apiService.setToken(rawJwt);
      }

      let profilePicture: { url: string; publicId?: string } | undefined;
      if (imageUri) {
        const upload = await apiService.uploadImage(imageUri, 'profile');
        if (!upload.success || !upload.data?.url) {
          Alert.alert('Error', upload.message || 'Failed to upload profile picture');
          setSaving(false);
          return;
        }
        profilePicture = { url: upload.data.url, publicId: upload.data.publicId };
      }

      const payload: Parameters<typeof apiService.updateProfile>[0] = {
        name: name.trim(),
        username: username.trim(),
        address: address.trim(),
        phoneNumber: selectedCountry
          ? selectedCountry.callingCode.replace(/\s+/g, '') +
            phoneNumber.replace(/\s+/g, '')
          : phoneNumber,
      };
      if (profilePicture) {
        payload.profilePicture = profilePicture;
      }

      const result = await apiService.updateProfile(payload);

      if (!result.success) {
        Alert.alert('Error', result.message || 'Failed to update profile');
        return;
      }

      // Refresh AuthContext so the new profile picture (and any other field)
      // shows up immediately in the rest of the app.
      if (result.data) {
        setCurrentUser(result.data);
      }

      Alert.alert('Success', 'Profile updated successfully');
      router.push('/profile');
    } catch (error: any) {
      Alert.alert('Error', error?.message || 'Failed to update profile');
    } finally {
      setSaving(false);
    }
  };

  return (
    <KeyboardAvoidingView
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      style={styles.container}
    >
      <ScrollView contentContainerStyle={styles.scrollContent}>
        {/* Avatar Section */}
        <View style={styles.avatarContainer}>
          <TouchableOpacity onPress={pickImageAsync} style={styles.avatarWrapper}>
            {profileImage ? (
              <Image source={{ uri: profileImage }} style={styles.avatar} />
            ) : (
              <View style={styles.avatarFallback}>
                <Text style={styles.avatarInitial}>
                  {user.name?.[0]?.toUpperCase()}
                </Text>
              </View>
            )}
            <View style={styles.cameraIcon}>
              <Ionicons name="camera" size={14} color="#666" />
            </View>
          </TouchableOpacity>
        </View>

        {/* Full Name */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Full Name</Text>
          <TextInput
            style={styles.input}
            value={name}
            onChangeText={setName}
            placeholder="Your Name"
          />
        </View>

        {/* Username */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Username</Text>
          <TextInput
            style={styles.input}
            value={username}
            onChangeText={setUsername}
            placeholder="Username"
          />
        </View>

        {/* Phone Number */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Phone Number</Text>
          <PhoneInput
            value={phoneNumber}
            onChangePhoneNumber={setPhoneNumber}
            selectedCountry={selectedCountry}
            onChangeSelectedCountry={setSelectedCountry}
            defaultCountry={defaultCca2}
            placeholder="7034567897"
            phoneInputStyles={{
              container: {
                borderWidth: 1.5,
                borderColor: '#e0e0e0',
                borderRadius: 10,
                backgroundColor: '#fff',
              },
              input: {
                fontSize: 15,
                color: '#333',
              },
            }}
          />
        </View>

        {/* Address */}
        <View style={styles.fieldGroup}>
          <Text style={styles.label}>Address</Text>
          <TextInput
            style={styles.input}
            value={address}
            onChangeText={setAddress}
            placeholder="Your Address"
            multiline
          />
        </View>

        {/* Save Button */}
        <TouchableOpacity
          style={[styles.button, saving && styles.buttonDisabled]}
          onPress={handleUpdate}
          disabled={saving}
        >
          {saving ? (
            <ActivityIndicator color="#fff" />
          ) : (
            <Text style={styles.buttonText}>Save Changes</Text>
          )}
        </TouchableOpacity>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  scrollContent: {
    padding: 20,
    paddingBottom: 40,
  },
  avatarContainer: {
    alignItems: 'center',
    marginBottom: 24,
  },
  avatarWrapper: {
    width: 90,
    height: 90,
    borderRadius: 45,
    position: 'relative',
  },
  avatar: {
    width: 90,
    height: 90,
    borderRadius: 45,
  },
  avatarFallback: {
    width: 90,
    height: 90,
    borderRadius: 45,
    backgroundColor: COLORS.primary,
    alignItems: 'center',
    justifyContent: 'center',
  },
  avatarInitial: {
    fontSize: 32,
    fontWeight: 'bold',
    color: '#fff',
  },
  cameraIcon: {
    position: 'absolute',
    bottom: 0,
    right: 0,
    width: 28,
    height: 28,
    borderRadius: 14,
    backgroundColor: '#fff',
    alignItems: 'center',
    justifyContent: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.2,
    shadowRadius: 2,
    elevation: 3,
  },
  fieldGroup: {
    marginBottom: 16,
  },
  label: {
    fontSize: 13,
    fontWeight: '500',
    color: '#666',
    marginBottom: 6,
  },
  input: {
    borderWidth: 1.5,
    borderColor: '#e0e0e0',
    borderRadius: 10,
    padding: 12,
    paddingHorizontal: 14,
    fontSize: 15,
    color: '#333',
    backgroundColor: '#fff',
  },
  button: {
    backgroundColor: COLORS.primary,
    alignItems: 'center',
    justifyContent: 'center',
    height: 50,
    borderRadius: 12,
    marginTop: 8,
  },
  buttonDisabled: {
    opacity: 0.7,
  },
  buttonText: {
    color: '#fff',
    fontSize: 15,
    fontWeight: '600',
  },
});

export default EditProfileScreen;
