// app/kyc/index.tsx
import React, { useState, useEffect } from 'react';
import {
  View,
  StyleSheet,
  Text,
  Alert,
  ActivityIndicator,
} from 'react-native';
import { router, useLocalSearchParams } from 'expo-router';
import { LinearProgress } from 'react-native-elements';
import { COLORS } from '@/constants/theme';
import SignupScreen2 from '@/components/signup/SignupScreen2';
import SignupScreen3 from '@/components/signup/SignupScreen3';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { api } from '../services/api';
import { SafeAreaView } from "react-native-safe-area-context";
// Reuse the SignupData interface from signup
export interface SignupData {
  // Screen 2 data
  driversLicenseFront?: string;
  driversLicenseBack?: string;
  ninFront?: string;
  ninBack?: string;
  passportPhoto?: string;
  pvcCard?: string;
  bvn?: string;

  // Screen 3 data
  faceFront?: string;
  faceLeft?: string;
  faceRight?: string;
  faceUp?: string;
  faceDown?: string;
}

const KYCFlow = () => {
  const params = useLocalSearchParams();
  const [currentStep, setCurrentStep] = useState<'screen2' | 'screen3'>('screen2');
  const [signupData, setSignupData] = useState<SignupData>({});
  const [loading, setLoading] = useState(false);
  const [authToken, setAuthToken] = useState<string | null>(null);
  const [userId, setUserId] = useState<string | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);

  useEffect(() => {
    checkAuthAndProgress();
  }, []);

  const checkAuthAndProgress = async () => {
    try {
      const token = await AsyncStorage.getItem('authToken');
      const storedUserId = await AsyncStorage.getItem('userId');

      if (!token || !storedUserId) {
        Alert.alert(
          'Authentication Required',
          'Please login to continue with KYC.',
          [
            {
              text: 'Go to Login',
              onPress: () => router.replace('/login')
            }
          ]
        );
        return;
      }

      setAuthToken(token);
      setUserId(storedUserId);
      api.setToken(token);

      // Load any existing KYC data
      const savedData = await AsyncStorage.getItem('kycData');
      if (savedData) {
        setSignupData(JSON.parse(savedData));
      }

      // Check if user came from specific step
      const step = params.step as string;
      if (step === 'screen3') {
        setCurrentStep('screen3');
      }
    } catch (error) {
      console.error('Error checking auth:', error);
      Alert.alert('Error', 'Failed to load KYC session');
    } finally {
      setInitialLoading(false);
    }
  };

  const updateSignupData = async (data: Partial<SignupData>) => {
    const updatedData = { ...signupData, ...data };
    setSignupData(updatedData);

    try {
      await AsyncStorage.setItem('kycData', JSON.stringify(updatedData));
    } catch (error) {
      console.error('Error saving KYC data:', error);
    }
  };

  const handleScreen2Submit = async () => {
    if (!authToken || !userId) {
      Alert.alert(
        'Authentication Required',
        'Please login again.',
        [
          {
            text: 'OK',
            onPress: () => router.replace('/login')
          }
        ]
      );
      return;
    }

    setLoading(true);
    try {
      const uploadedDocuments: any = {};
      const uploadErrors: string[] = [];

      const uploadDocumentImage = async (field: keyof SignupData, type: string, label: string) => {
        const imageUri = signupData[field] as string;
        if (imageUri) {
          try {
            const result = await api.uploadImage(imageUri, 'document');
            if (result.success) {
              //@ts-expect-error
              uploadedDocuments[field] = result.data.url;
            }
          } catch (uploadError) {
            console.error(`Failed to upload ${label}:`, uploadError);
            uploadErrors.push(`${label} upload failed`);
          }
        }
      };

      const uploadPromises = [
        uploadDocumentImage('driversLicenseFront', 'drivers_license', "Driver's License Front"),
        uploadDocumentImage('driversLicenseBack', 'drivers_license', "Driver's License Back"),
        uploadDocumentImage('ninFront', 'nin', 'NIN Front'),
        uploadDocumentImage('ninBack', 'nin', 'NIN Back'),
        uploadDocumentImage('passportPhoto', 'passport', 'Passport Photo'),
        uploadDocumentImage('pvcCard', 'pvc', 'PVC Card'),
      ];

      await Promise.all(uploadPromises);

      const hasUploadedDocuments = Object.keys(uploadedDocuments).length > 0;
      const hasBvn = signupData.bvn && signupData.bvn.trim().length === 11;

      if (!hasUploadedDocuments && !hasBvn) {
        Alert.alert(
          'Documents Required',
          'Please upload at least one ID document or provide your BVN.',
          [{ text: 'OK' }]
        );
        setLoading(false);
        return;
      }

      const identityPayload: any = {};

      Object.keys(uploadedDocuments).forEach(key => {
        identityPayload[key] = uploadedDocuments[key];
      });

      if (signupData.bvn) {
        identityPayload.bvn = signupData.bvn.trim();
      }

      const result = await api.updateIdentityDocuments(identityPayload);

      if (result.success) {
        setCurrentStep('screen3');

        if (uploadErrors.length > 0) {
          Alert.alert(
            'Partial Success',
            `Some documents failed to upload, but you can continue. Failed: ${uploadErrors.join(', ')}`,
            [{ text: 'Continue' }]
          );
        }
      } else {
        Alert.alert('Upload Failed', result.message || 'Failed to upload documents');
      }
    } catch (error: any) {
      console.error('Identity upload error:', error);
      Alert.alert('Error', 'Failed to upload documents. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleScreen3Submit = async () => {
    if (!authToken || !userId) {
      Alert.alert(
        'Authentication Required',
        'Please login again.',
        [
          {
            text: 'OK',
            onPress: () => router.replace('/login')
          }
        ]
      );
      return;
    }

    setLoading(true);
    try {
      const facialImages = [
        { key: 'faceFront', uri: signupData.faceFront },
        { key: 'faceLeft', uri: signupData.faceLeft },
        { key: 'faceRight', uri: signupData.faceRight },
        { key: 'faceUp', uri: signupData.faceUp },
        { key: 'faceDown', uri: signupData.faceDown },
      ];

      const missingAngles = facialImages.filter(img => !img.uri);

      if (missingAngles.length > 0) {
        Alert.alert(
          'Incomplete',
          `Please capture all 5 facial angles. Missing: ${missingAngles.map(img => img.key.replace('face', '')).join(', ')}`,
          [{ text: 'OK' }]
        );
        setLoading(false);
        return;
      }

      const imageUris = facialImages.map(img => img.uri as string);
      const uploadResult = await api.uploadImages(imageUris, 'facial');

      if (!uploadResult.success) {
        throw new Error(uploadResult.message || 'Failed to upload facial images');
      }

      //@ts-expect-error
      const uploadedUrls = uploadResult.data.uploaded;
      const facialPayload = {
        faceFront: uploadedUrls[0]?.url,
        faceLeft: uploadedUrls[1]?.url,
        faceRight: uploadedUrls[2]?.url,
        faceUp: uploadedUrls[3]?.url,
        faceDown: uploadedUrls[4]?.url,
      };

      const result = await api.updateFacialVerification(facialPayload);

      if (result.success) {
        const verificationResult = await api.submitForVerification();

        // Clear KYC data
        await AsyncStorage.removeItem('kycData');

        if (verificationResult.success) {
          Alert.alert(
            '🎉 KYC Completed!',
            'Your KYC verification is complete and submitted for review.',
            [
              {
                text: 'Go to Dashboard',
                onPress: () => router.replace('/home')
              }
            ]
          );
        } else {
          Alert.alert(
            '✅ KYC Submitted',
            'Your KYC documents have been submitted for verification.',
            [
              {
                text: 'Go to Dashboard',
                onPress: () => router.replace('/home')
              }
            ]
          );
        }
      } else {
        Alert.alert('Upload Failed', result.message || 'Failed to upload facial verification');
      }
    } catch (error: any) {
      console.error('Facial verification error:', error);
      Alert.alert('Error', 'Failed to complete facial verification. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleBack = () => {
    if (currentStep === 'screen3') {
      setCurrentStep('screen2');
    } else {
      router.back();
    }
  };

  const renderStep = () => {
    switch (currentStep) {
      case 'screen2':
        return (
          <SignupScreen2
            data={signupData}
            updateData={updateSignupData}
            onNext={handleScreen2Submit}
            onBack={handleBack}
            isLoading={loading}
            authToken={authToken}
            userId={userId}
            isStandaloneKYC={true}
          />
        );
      case 'screen3':
        return (
          <SignupScreen3
            data={signupData}
            updateData={updateSignupData}
            onNext={handleScreen3Submit}
            onBack={handleBack}
            isLoading={loading}
            authToken={authToken}
            userId={userId}
            isStandaloneKYC={true}
          />
        );
      default:
        return null;
    }
  };

  if (initialLoading) {
    return (
      <SafeAreaView style={styles.container}>
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={COLORS.primary} />
          <Text style={styles.loadingText}>Loading KYC...</Text>
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.container}>
      {/* Progress Bar */}
      <View style={styles.progressContainer}>
        <LinearProgress
          value={currentStep === 'screen2' ? 0.5 : 1}
          color={COLORS.primary}
          variant="determinate"
          style={styles.progressBar}
        />
        <View style={styles.stepIndicator}>
          <Text style={styles.stepText}>
            {currentStep === 'screen2' ? 'Step 1 of 2 - Identity Documents' : 'Step 2 of 2 - Facial Verification'}
          </Text>
        </View>
      </View>

      {/* Loading Overlay */}
      {loading && (
        <View style={styles.globalLoadingOverlay}>
          <View style={styles.loadingContent}>
            <ActivityIndicator size="large" color={COLORS.primary} />
            <Text style={styles.globalLoadingText}>
              {currentStep === 'screen2' ? 'Uploading documents...' : 'Processing facial verification...'}
            </Text>
          </View>
        </View>
      )}

      {renderStep()}
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  progressContainer: {
    paddingHorizontal: 20,
    paddingTop: 20,
    paddingBottom: 10,
    backgroundColor: '#f8f9fa',
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  progressBar: {
    height: 6,
    borderRadius: 3,
  },
  stepIndicator: {
    alignItems: 'center',
    marginTop: 12,
  },
  stepText: {
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '600',
  },
  globalLoadingOverlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(255, 255, 255, 0.95)',
    justifyContent: 'center',
    alignItems: 'center',
    zIndex: 1000,
  },
  loadingContent: {
    alignItems: 'center',
    padding: 30,
    backgroundColor: '#fff',
    borderRadius: 16,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 8,
    elevation: 5,
  },
  globalLoadingText: {
    marginTop: 20,
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: '#fff',
  },
  loadingText: {
    marginTop: 20,
    fontSize: 16,
    color: COLORS.gray,
  },
});

export default KYCFlow;