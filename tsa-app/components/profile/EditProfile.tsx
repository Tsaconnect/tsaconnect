// components/profile/EditProfile.tsx
//
// Edit-profile form (avatar picker + name/username/phone/address fields + save).
// Migrated onto the TSA Signature design system (theme tokens via useTheme() +
// @/components/ui primitives). The component name, props, exports, hook order,
// image-picker flow, token handling, updateProfile payload shape, AuthContext
// refresh, alerts, and the `/profile` navigation are all preserved verbatim —
// this is a visual / structural restyle only.
import React, { useState } from 'react';
import {
  View,
  TouchableOpacity,
  Alert,
  ScrollView,
  KeyboardAvoidingView,
  Platform,
  Image,
} from 'react-native';
import { useRouter } from 'expo-router';
import PhoneInput from 'react-native-international-phone-number';
import { useTheme } from '@/theme';
import { Button, Icon, Input, Text } from '@/components/ui';
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
  const { colors, spacing, radius, fonts, type } = useTheme();
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
      style={{ flex: 1, backgroundColor: colors.background }}
    >
      <ScrollView contentContainerStyle={{ padding: spacing.xl, paddingBottom: spacing['4xl'] }}>
        {/* Avatar Section */}
        <View style={{ alignItems: 'center', marginBottom: spacing['2xl'] }}>
          <TouchableOpacity
            onPress={pickImageAsync}
            style={{ width: 90, height: 90, borderRadius: 45, position: 'relative' }}
          >
            {profileImage ? (
              <Image
                source={{ uri: profileImage }}
                style={{ width: 90, height: 90, borderRadius: 45 }}
              />
            ) : (
              <View
                style={{
                  width: 90,
                  height: 90,
                  borderRadius: 45,
                  backgroundColor: colors.accent,
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <Text style={{ fontSize: 32, fontFamily: fonts['700'], color: colors.onAccent }}>
                  {user.name?.[0]?.toUpperCase()}
                </Text>
              </View>
            )}
            <View
              style={{
                position: 'absolute',
                bottom: 0,
                right: 0,
                width: 28,
                height: 28,
                borderRadius: 14,
                backgroundColor: colors.surface,
                alignItems: 'center',
                justifyContent: 'center',
                shadowColor: '#000',
                shadowOffset: { width: 0, height: 1 },
                shadowOpacity: 0.2,
                shadowRadius: 2,
                elevation: 3,
              }}
            >
              <Icon name="camera" size={14} color={colors.textSecondary} />
            </View>
          </TouchableOpacity>
        </View>

        {/* Full Name */}
        <Input
          label="Full Name"
          value={name}
          onChangeText={setName}
          placeholder="Your Name"
          containerStyle={{ marginBottom: spacing.lg }}
        />

        {/* Username */}
        <Input
          label="Username"
          value={username}
          onChangeText={setUsername}
          placeholder="Username"
          containerStyle={{ marginBottom: spacing.lg }}
        />

        {/* Phone Number */}
        <View style={{ marginBottom: spacing.lg }}>
          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
            Phone Number
          </Text>
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
                borderColor: colors.border,
                borderRadius: radius.md,
                backgroundColor: colors.surface,
              },
              input: {
                fontSize: type.body.fontSize,
                color: colors.textPrimary,
              },
            }}
          />
        </View>

        {/* Address */}
        <Input
          label="Address"
          value={address}
          onChangeText={setAddress}
          placeholder="Your Address"
          multiline
          containerStyle={{ marginBottom: spacing.lg }}
        />

        {/* Save Button */}
        <Button
          title="Save Changes"
          onPress={handleUpdate}
          loading={saving}
          disabled={saving}
          style={{ marginTop: spacing.sm }}
        />
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default EditProfileScreen;
