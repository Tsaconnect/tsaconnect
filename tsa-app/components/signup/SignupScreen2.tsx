// components/signup/SignupScreen2.tsx
import React, { useState } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  Image,
  TextInput,
  ActivityIndicator,
  Alert,
} from 'react-native';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import * as ImagePicker from 'expo-image-picker';
import { COLORS } from '../../constants/theme';
import { SignupData } from './signup';

interface SignupScreen2Props {
  data: SignupData;
  updateData: (data: Partial<SignupData>) => void;
  onNext: () => void;
  onBack: () => void;
  isLoading?: boolean;
  authToken?: string | null;
  userId?: string | null;
}

const SignupScreen2: React.FC<SignupScreen2Props> = ({
  data,
  updateData,
  onNext,
  onBack,
  isLoading = false,
  authToken,
  userId,
}) => {
  const [uploadingImage, setUploadingImage] = useState<string | null>(null);

  const pickImage = async (field: keyof SignupData, label: string) => {
    if (isLoading) return;

    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== 'granted') {
      Alert.alert(
        'Permission Required',
        'Sorry, we need camera roll permissions to make this work!',
        [{ text: 'OK' }]
      );
      return;
    }

    setUploadingImage(field);
    
    try {
      const result = await ImagePicker.launchImageLibraryAsync({
        mediaTypes: ImagePicker.MediaTypeOptions.Images,
        allowsEditing: true,
        aspect: [3, 2],
        quality: 0.8,
      });

      if (!result.canceled) {
        updateData({ [field]: result.assets[0].uri });
        Alert.alert('Success', `${label} selected successfully`);
      }
    } catch (error) {
      console.error('Error picking image:', error);
      Alert.alert('Error', 'Failed to select image. Please try again.');
    } finally {
      setUploadingImage(null);
    }
  };

  const validateFields = () => {
    // Check if at least one document is provided or BVN is provided
    const hasDocument = 
      data.driversLicenseFront || 
      data.driversLicenseBack || 
      data.ninFront || 
      data.ninBack || 
      data.passportPhoto || 
      data.pvcCard;

    const hasBvn = data.bvn && data.bvn.trim().length === 11;

    if (!hasDocument && !hasBvn) {
      Alert.alert(
        'Required',
        'Please upload at least one ID document or provide your BVN.'
      );
      return false;
    }

    // Validate BVN if provided
    if (data.bvn && data.bvn.trim().length !== 11) {
      Alert.alert('Invalid BVN', 'BVN must be exactly 11 digits.');
      return false;
    }

    return true;
  };

  const handleNext = () => {
    if (validateFields()) {
      onNext();
    }
  };

  const renderUploadSection = (
    title: string,
    fields: { label: string; field: keyof SignupData }[],
    icon: string
  ) => (
    <View style={styles.uploadSection}>
      <View style={styles.sectionHeader}>
        <MaterialIcons name={icon as any} size={24} color={COLORS.primary} />
        <Text style={styles.sectionTitle}>{title}</Text>
      </View>
      <View style={styles.uploadGrid}>
        {fields.map((item) => (
          <View key={item.field} style={styles.uploadItem}>
            <Text style={styles.uploadLabel}>{item.label}</Text>
            <TouchableOpacity
              style={styles.uploadButton}
              onPress={() => pickImage(item.field, item.label)}
              disabled={isLoading || uploadingImage === item.field}
            >
              {uploadingImage === item.field ? (
                <View style={styles.uploadingContainer}>
                  <ActivityIndicator size="small" color={COLORS.primary} />
                  <Text style={styles.uploadingText}>Loading...</Text>
                </View>
              ) : data[item.field] ? (
                <Image
                  source={{ uri: data[item.field] as string }}
                  style={styles.uploadedImage}
                />
              ) : (
                <View style={styles.uploadPlaceholder}>
                  <MaterialIcons 
                    name="add-photo-alternate" 
                    size={30} 
                    color={isLoading ? COLORS.lightGray : COLORS.gray} 
                  />
                </View>
              )}
            </TouchableOpacity>
            {data[item.field] && (
              <TouchableOpacity
                style={styles.clearButton}
                onPress={() => updateData({ [item.field]: undefined })}
                disabled={isLoading}
              >
                <MaterialIcons name="close" size={16} color={COLORS.danger} />
              </TouchableOpacity>
            )}
          </View>
        ))}
      </View>
    </View>
  );

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      <View style={styles.header}>
        <TouchableOpacity onPress={onBack} style={styles.backButton} disabled={isLoading}>
          <MaterialIcons name="arrow-back" size={24} color={isLoading ? COLORS.lightGray : COLORS.primary} />
        </TouchableOpacity>
        <Text style={styles.title}>Identity Verification</Text>
        <Text style={styles.subtitle}>Upload at least one valid ID or provide BVN</Text>
        
        {authToken && userId && (
          <View style={styles.authStatus}>
            <MaterialIcons name="verified" size={16} color={COLORS.success} />
            <Text style={styles.authStatusText}>Step 1 completed ✓</Text>
          </View>
        )}
      </View>

      {/* Driver's License */}
      {renderUploadSection(
        "Driver's License",
        [
          { label: 'Front Side', field: 'driversLicenseFront' },
          { label: 'Back Side', field: 'driversLicenseBack' },
        ],
        'directions-car'
      )}

      {/* NIN Slip/Card */}
      {renderUploadSection(
        'NIN Slip/Card',
        [
          { label: 'Front Side', field: 'ninFront' },
          { label: 'Back Side', field: 'ninBack' },
        ],
        'badge'
      )}

      {/* International Passport */}
      <View style={styles.uploadSection}>
        <View style={styles.sectionHeader}>
          <MaterialIcons name="card-travel" size={24} color={COLORS.primary} />
          <Text style={styles.sectionTitle}>International Passport</Text>
        </View>
        <View style={styles.uploadItem}>
          <Text style={styles.uploadLabel}>Photo Page</Text>
          <TouchableOpacity
            style={styles.uploadButton}
            onPress={() => pickImage('passportPhoto', 'Passport Photo')}
            disabled={isLoading || uploadingImage === 'passportPhoto'}
          >
            {uploadingImage === 'passportPhoto' ? (
              <View style={styles.uploadingContainer}>
                <ActivityIndicator size="small" color={COLORS.primary} />
                <Text style={styles.uploadingText}>Loading...</Text>
              </View>
            ) : data.passportPhoto ? (
              <Image
                source={{ uri: data.passportPhoto }}
                style={styles.uploadedImage}
              />
            ) : (
              <View style={styles.uploadPlaceholder}>
                <MaterialIcons 
                  name="add-photo-alternate" 
                  size={30} 
                  color={isLoading ? COLORS.lightGray : COLORS.gray} 
                />
              </View>
            )}
          </TouchableOpacity>
          {data.passportPhoto && (
            <TouchableOpacity
              style={styles.clearButton}
              onPress={() => updateData({ passportPhoto: undefined })}
              disabled={isLoading}
            >
              <MaterialIcons name="close" size={16} color={COLORS.danger} />
            </TouchableOpacity>
          )}
        </View>
      </View>

      {/* PVC Card */}
      <View style={styles.uploadSection}>
        <View style={styles.sectionHeader}>
          <MaterialIcons name="how-to-vote" size={24} color={COLORS.primary} />
          <Text style={styles.sectionTitle}>Permanent Voter's Card (PVC)</Text>
        </View>
        <View style={styles.uploadItem}>
          <Text style={styles.uploadLabel}>PVC Card</Text>
          <TouchableOpacity
            style={styles.uploadButton}
            onPress={() => pickImage('pvcCard', 'PVC Card')}
            disabled={isLoading || uploadingImage === 'pvcCard'}
          >
            {uploadingImage === 'pvcCard' ? (
              <View style={styles.uploadingContainer}>
                <ActivityIndicator size="small" color={COLORS.primary} />
                <Text style={styles.uploadingText}>Loading...</Text>
              </View>
            ) : data.pvcCard ? (
              <Image
                source={{ uri: data.pvcCard }}
                style={styles.uploadedImage}
              />
            ) : (
              <View style={styles.uploadPlaceholder}>
                <MaterialIcons 
                  name="add-photo-alternate" 
                  size={30} 
                  color={isLoading ? COLORS.lightGray : COLORS.gray} 
                />
              </View>
            )}
          </TouchableOpacity>
          {data.pvcCard && (
            <TouchableOpacity
              style={styles.clearButton}
              onPress={() => updateData({ pvcCard: undefined })}
              disabled={isLoading}
            >
              <MaterialIcons name="close" size={16} color={COLORS.danger} />
            </TouchableOpacity>
          )}
        </View>
      </View>

      {/* BVN Input */}
      <View style={styles.uploadSection}>
        <View style={styles.sectionHeader}>
          <MaterialIcons name="security" size={24} color={COLORS.primary} />
          <Text style={styles.sectionTitle}>Bank Verification Number (BVN)</Text>
        </View>
        <View style={styles.inputContainer}>
          <MaterialIcons name="credit-card" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Enter 11-digit BVN (Optional)"
            value={data.bvn}
            onChangeText={(text) => updateData({ bvn: text.replace(/[^0-9]/g, '') })}
            keyboardType="numeric"
            maxLength={11}
            editable={!isLoading}
          />
        </View>
        <Text style={styles.bvnNote}>
          Your BVN is encrypted and stored securely. It's optional but recommended for faster verification.
        </Text>
      </View>

      {/* Verification Status */}
      <View style={styles.statusContainer}>
        <MaterialIcons name="verified" size={24} color={COLORS.success} />
        <View style={styles.statusContent}>
          <Text style={styles.statusTitle}>Secure & Encrypted</Text>
          <Text style={styles.statusText}>
            Your documents are encrypted and stored securely. We only use them for verification purposes.
          </Text>
        </View>
      </View>

      {/* Next Button */}
      <TouchableOpacity 
        style={[styles.nextButton, isLoading && styles.nextButtonDisabled]} 
        onPress={handleNext}
        disabled={isLoading}
      >
        {isLoading ? (
          <ActivityIndicator size="small" color="#fff" />
        ) : (
          <>
            <Text style={styles.nextButtonText}>Next</Text>
            <MaterialIcons name="arrow-forward" size={20} color="#fff" />
          </>
        )}
      </TouchableOpacity>

      {/* Progress Note */}
      <View style={styles.progressNote}>
        <MaterialIcons name="info" size={16} color={COLORS.primary} />
        <Text style={styles.progressNoteText}>
          Step 2 of 3 - Next: Facial Verification
        </Text>
      </View>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 20,
    backgroundColor: '#fff',
  },
  header: {
    marginBottom: 30,
  },
  backButton: {
    marginBottom: 20,
  },
  title: {
    fontSize: 28,
    fontWeight: '700',
    color: COLORS.primary,
    marginBottom: 8,
  },
  subtitle: {
    fontSize: 16,
    color: COLORS.gray,
    marginBottom: 8,
  },
  authStatus: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(16, 185, 129, 0.1)',
    borderRadius: 8,
    padding: 8,
    alignSelf: 'flex-start',
  },
  authStatusText: {
    marginLeft: 6,
    fontSize: 14,
    color: COLORS.success,
    fontWeight: '500',
  },
  uploadSection: {
    marginBottom: 30,
    backgroundColor: '#f8f9fa',
    borderRadius: 16,
    padding: 20,
  },
  sectionHeader: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 20,
  },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.dark,
    marginLeft: 10,
  },
  uploadGrid: {
    flexDirection: 'row',
    justifyContent: 'space-between',
  },
  uploadItem: {
    flex: 1,
    marginHorizontal: 5,
    position: 'relative',
  },
  uploadLabel: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 10,
    textAlign: 'center',
  },
  uploadButton: {
    aspectRatio: 3/2,
    borderRadius: 12,
    overflow: 'hidden',
    backgroundColor: '#fff',
    borderWidth: 2,
    borderColor: COLORS.lightGray,
    borderStyle: 'dashed',
  },
  uploadingContainer: {
    width: '100%',
    height: '100%',
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#f8f9fa',
  },
  uploadingText: {
    marginTop: 8,
    fontSize: 12,
    color: COLORS.primary,
  },
  uploadedImage: {
    width: '100%',
    height: '100%',
  },
  uploadPlaceholder: {
    width: '100%',
    height: '100%',
    justifyContent: 'center',
    alignItems: 'center',
  },
  clearButton: {
    position: 'absolute',
    top: 5,
    right: 5,
    backgroundColor: 'rgba(255, 255, 255, 0.9)',
    borderRadius: 12,
    width: 24,
    height: 24,
    justifyContent: 'center',
    alignItems: 'center',
    zIndex: 1,
  },
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    paddingHorizontal: 15,
    height: 56,
    marginBottom: 10,
  },
  inputIcon: {
    marginRight: 10,
  },
  input: {
    flex: 1,
    fontSize: 16,
    color: COLORS.dark,
  },
  bvnNote: {
    fontSize: 12,
    color: COLORS.gray,
    fontStyle: 'italic',
    marginTop: 5,
  },
  statusContainer: {
    flexDirection: 'row',
    alignItems: 'flex-start',
    backgroundColor: 'rgba(76, 217, 100, 0.1)',
    borderRadius: 12,
    padding: 15,
    marginBottom: 30,
  },
  statusContent: {
    flex: 1,
    marginLeft: 10,
  },
  statusTitle: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.success,
    marginBottom: 4,
  },
  statusText: {
    fontSize: 14,
    color: COLORS.dark,
    lineHeight: 20,
  },
  nextButton: {
    flexDirection: 'row',
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    height: 56,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 20,
    shadowColor: COLORS.primary,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 5,
  },
  nextButtonDisabled: {
    backgroundColor: COLORS.lightGray,
    opacity: 0.7,
  },
  nextButtonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
    marginRight: 10,
  },
  progressNote: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: 30,
  },
  progressNoteText: {
    marginLeft: 8,
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '500',
  },
});

export default SignupScreen2;