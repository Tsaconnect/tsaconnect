import React, { useState, useEffect } from 'react';
import {
  View,
  StyleSheet,
  Text,
  Alert,
  ActivityIndicator,
  TouchableOpacity,
} from 'react-native';
import { router } from 'expo-router';
import { LinearProgress } from 'react-native-elements';
import { COLORS } from '@/constants/theme';
import SignupScreen1 from '@/components/signup/SignupScreen1';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { api } from '../services/api';
import { Ionicons } from '@expo/vector-icons';
import { SafeAreaView } from "react-native-safe-area-context";

export interface SignupData {
  // Screen 1 data
  name: string;
  username: string;
  email: string;
  password: string;
  confirmPassword: string;
  phoneNumber: string;
  country: string;
  state: string;
  city: string;
  address: string;
  profilePhoto: string | null;
  referralCode?: string;

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

const SignupFlow = () => {
  const [currentStep, setCurrentStep] = useState<'screen1' | 'screen2' | 'screen3' | 'kyc_choice'>('screen1');
  const [signupData, setSignupData] = useState<SignupData>({
    name: '',
    username: '',
    email: '',
    password: '',
    confirmPassword: '',
    phoneNumber: '',
    country: '',
    state: '',
    city: '',
    address: '',
    profilePhoto: null,
    referralCode: '',
  });
  const [loading, setLoading] = useState(false);
  const [authToken, setAuthToken] = useState<string | null>(null);
  const [userId, setUserId] = useState<string | null>(null);
  const [initialLoading, setInitialLoading] = useState(true);
  const [isKycCompleted, setIsKycCompleted] = useState(false);

  // Check for existing auth on mount
  useEffect(() => {
    checkExistingAuth();
  }, []);

  const checkExistingAuth = async () => {
    try {
      const token = await AsyncStorage.getItem('authToken');
      const storedUserId = await AsyncStorage.getItem('userId');

      if (token && storedUserId) {
        setAuthToken(token);
        setUserId(storedUserId);
        api.setToken(token);

        // Check if KYC is already completed
        const kycStatus = await AsyncStorage.getItem('kycCompleted');
        if (kycStatus === 'true') {
          setIsKycCompleted(true);
        }

        // Check which step they were on
        const step = await AsyncStorage.getItem('currentSignupStep');
        if (step) {
          if (step === 'screen2' || step === 'screen3') {
            setCurrentStep(step as 'screen2' | 'screen3');

            // Load saved data if exists
            const savedData = await AsyncStorage.getItem('signupData');
            if (savedData) {
              setSignupData(JSON.parse(savedData));
            }
          } else if (step === 'kyc_choice') {
            setCurrentStep('kyc_choice');
          }
        }
      }
    } catch (error) {
      console.error('Error checking existing auth:', error);
    } finally {
      setInitialLoading(false);
    }
  };

  const updateSignupData = async (data: Partial<SignupData>) => {
    const updatedData = { ...signupData, ...data };
    setSignupData(updatedData);

    try {
      await AsyncStorage.setItem('signupData', JSON.stringify(updatedData));
    } catch (error) {
      console.error('Error saving signup data:', error);
    }
  };

  const saveProgress = async (step: string) => {
    try {
      await AsyncStorage.setItem('currentSignupStep', step);
    } catch (error) {
      console.error('Error saving progress:', error);
    }
  };

  const clearProgress = async () => {
    try {
      await AsyncStorage.removeItem('currentSignupStep');
      await AsyncStorage.removeItem('signupData');
    } catch (error) {
      console.error('Error clearing progress:', error);
    }
  };

  const markKycCompleted = async () => {
    try {
      setIsKycCompleted(true);
      await AsyncStorage.setItem('kycCompleted', 'true');
    } catch (error) {
      console.error('Error marking KYC completed:', error);
    }
  };

  const handleScreen1Submit = async () => {
    setLoading(true);
    try {
      // Upload profile photo if exists
      let profilePhotoUrl = null;
      if (signupData.profilePhoto) {
        try {
          const uploadResult = await api.uploadImage(signupData.profilePhoto, 'profile');
          if (uploadResult.success) {
            //@ts-ignore
            profilePhotoUrl = uploadResult.data.url;
          }
        } catch (uploadError) {
          console.warn('Profile photo upload failed, continuing without it:', uploadError);
        }
      }

      // Prepare signup data
      const signupPayload = {
        name: signupData.name.trim(),
        username: signupData.username.trim().toLowerCase(),
        email: signupData.email.trim().toLowerCase(),
        password: signupData.password,
        confirmPassword: signupData.confirmPassword,
        phoneNumber: signupData.phoneNumber.trim(),
        country: signupData.country,
        state: signupData.state || undefined,
        city: signupData.city || undefined,
        address: signupData.address.trim(),
        profilePhoto: profilePhotoUrl,
        referralCode: signupData.referralCode?.trim() || undefined,
      };

      console.log('Submitting signup data...');

      // Call signup API
      //@ts-ignore
      const result = await api.signup(signupPayload);

      if (result.success) {
        // Store token and user ID
        //@ts-ignore
        const token = result.data.token;
        //@ts-ignore
        const userId = result.data.userId;

        setAuthToken(token);
        setUserId(userId);

        await AsyncStorage.setItem('authToken', token);
        await AsyncStorage.setItem('userId', userId);

        api.setToken(token);

        // Save progress and move to KYC choice
        await saveProgress('kyc_choice');
        setCurrentStep('kyc_choice');

        console.log('Signup successful, showing KYC choice');
      } else {
        const errorMessage = result.message || 'Please check your information';
        Alert.alert('Signup Failed', errorMessage);

        if (result.errors && result.errors.length > 0) {
          const fieldErrors = result.errors.map((err: any) =>
            `• ${err.field}: ${err.message}`
          ).join('\n');

          Alert.alert('Validation Errors', fieldErrors);
        }
      }
    } catch (error: any) {
      console.error('Signup error:', error);

      let errorMessage = 'Failed to sign up. Please try again.';
      if (error.message.includes('Network request failed')) {
        errorMessage = 'Network error. Please check your internet connection.';
      } else if (error.message.includes('timeout')) {
        errorMessage = 'Request timeout. Please try again.';
      }

      Alert.alert('Error', errorMessage);
    } finally {
      setLoading(false);
    }
  };

  const handleContinueToDashboard = async () => {
    // Mark that user has chosen to skip KYC for now
    await saveProgress('dashboard');

    // Clear signup progress
    await clearProgress();

    // Navigate to dashboard
    router.replace('/home');
  };

  const handleContinueKYC = async () => {
    await saveProgress('screen2');
    setCurrentStep('screen2');
  };

  const handleScreen2Submit = async () => {
    if (!authToken || !userId) {
      Alert.alert(
        'Authentication Required',
        'Please go back and complete the first step again.',
        [
          {
            text: 'Go Back',
            onPress: () => {
              setCurrentStep('screen1');
              saveProgress('screen1');
            }
          }
        ]
      );
      return;
    }

    setLoading(true);
    try {
      // Upload all document images
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

      console.log('Submitting identity documents...', identityPayload);

      const result = await api.updateIdentityDocuments(identityPayload);

      if (result.success) {
        await saveProgress('screen3');
        setCurrentStep('screen3');

        if (uploadErrors.length > 0) {
          Alert.alert(
            'Partial Success',
            `Some documents failed to upload, but you can continue. Failed: ${uploadErrors.join(', ')}`,
            [{ text: 'Continue' }]
          );
        }
      } else {
        const errorMessage = result.message || 'Failed to upload documents';
        Alert.alert('Upload Failed', errorMessage);

        if (result.errors && result.errors.length > 0) {
          const fieldErrors = result.errors.map((err: any) =>
            `• ${err.field}: ${err.message}`
          ).join('\n');

          Alert.alert('Upload Errors', fieldErrors);
        }
      }
    } catch (error: any) {
      console.error('Identity upload error:', error);

      let errorMessage = 'Failed to upload documents. Please try again.';
      if (error.message.includes('Network request failed')) {
        errorMessage = 'Network error. Please check your internet connection.';
      } else if (error.message.includes('401') || error.message.includes('token')) {
        errorMessage = 'Session expired. Please go back and start again.';
        await clearAuth();
      }
      Alert.alert('Error', errorMessage);
      router.push('/home')
    } finally {
      setLoading(false);
    }
  };

  const handleScreen3Submit = async () => {
    if (!authToken || !userId) {
      Alert.alert(
        'Authentication Required',
        'Please go back and complete the previous steps.',
        [
          {
            text: 'Go Back',
            onPress: () => {
              setCurrentStep('screen1');
              saveProgress('screen1');
            }
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

      console.log('Submitting facial verification...', facialPayload);

      const result = await api.updateFacialVerification(facialPayload);

      if (result.success) {
        console.log('Submitting for final verification...');
        alert('Submitting for final verification...')
        const verificationResult = await api.submitForVerification();

        if (verificationResult.success) {
          // Mark KYC as completed
          await markKycCompleted();

          Alert.alert(
            '🎉 KYC Completed!',
            'Your KYC verification is complete! Our team will review your documents within 24-48 hours.\n\nYou will receive an email once your account is fully verified.',
            [
              {
                text: 'Go to Dashboard',
                onPress: () => {
                  clearProgress();
                  router.replace('/home');
                }
              }
            ]
          );
        } else {
          await markKycCompleted();

          Alert.alert(
            '✅ KYC Submitted',
            'Your KYC documents have been submitted for verification.',
            [
              {
                text: 'Go to Dashboard',
                onPress: () => {
                  clearProgress();
                  router.replace('/home');
                }
              }
            ]
          );
        }
      } else {
        Alert.alert('Upload Failed', result.message || 'Failed to upload facial verification');
      }
    } catch (error: any) {
      let errorMessage = 'Failed to complete facial verification. Please try again.';
      if (error.message.includes('Network request failed')) {
        errorMessage = 'Network error. Please check your internet connection.';
      } else if (error.message.includes('401') || error.message.includes('token')) {
        errorMessage = 'Session expired. Please go back and start again.';
        await clearAuth();
      }
      Alert.alert('Error', errorMessage);
    } finally {
      setLoading(false);
    }
  };

  const handleBack = async () => {
    if (currentStep === 'screen2') {
      await saveProgress('kyc_choice');
      setCurrentStep('kyc_choice');
    } else if (currentStep === 'screen3') {
      await saveProgress('screen2');
      setCurrentStep('screen2');
    } else if (currentStep === 'kyc_choice') {
      await saveProgress('screen1');
      setCurrentStep('screen1');
    } else if (currentStep === 'screen1') {
      await clearProgress();
      router.back();
    }
  };

  const clearAuth = async () => {
    try {
      setAuthToken(null);
      setUserId(null);
      api.clearToken();
      await AsyncStorage.removeItem('authToken');
      await AsyncStorage.removeItem('userId');
    } catch (error) {
      console.error('Error clearing auth:', error);
    }
  };

  // Create separate KYC choice component
  const renderKYCChoice = () => (
    <View style={styles.kycChoiceContainer}>
      <View style={styles.kycChoiceHeader}>
        <Ionicons name="checkmark-circle" size={80} color={COLORS.success} />
        <Text style={styles.kycChoiceTitle}>Account Created Successfully!</Text>
        <Text style={styles.kycChoiceSubtitle}>
          Your account has been created. You can continue to the dashboard or complete KYC verification now.
        </Text>
      </View>

      <View style={styles.kycChoiceOptions}>
        <TouchableOpacity
          style={[styles.kycOptionCard, styles.dashboardOption]}
          onPress={handleContinueToDashboard}
        >
          <View style={styles.kycOptionIcon}>
            <Ionicons name="speedometer" size={32} color={COLORS.primary} />
          </View>
          <Text style={styles.kycOptionTitle}>Go to Dashboard</Text>
          <Text style={styles.kycOptionDescription}>
            Skip KYC for now and access your dashboard. You can complete KYC later from your profile.
          </Text>
          <Text style={styles.kycOptionNote}>
            ⚠️ Some features may be limited without KYC
          </Text>
        </TouchableOpacity>

        <TouchableOpacity
          style={[styles.kycOptionCard, styles.kycOption]}
          onPress={handleContinueKYC}
        >
          <View style={styles.kycOptionIcon}>
            <Ionicons name="shield-checkmark" size={32} color={COLORS.success} />
          </View>
          <Text style={styles.kycOptionTitle}>Complete KYC Now</Text>
          <Text style={styles.kycOptionDescription}>
            Complete identity and facial verification to unlock all features and higher transaction limits.
          </Text>
          <View style={styles.kycSteps}>
            <View style={styles.kycStep}>
              <Ionicons name="document-text" size={20} color={COLORS.primary} />
              <Text style={styles.kycStepText}>Identity Documents</Text>
            </View>
            <View style={styles.kycStep}>
              <Ionicons name="camera" size={20} color={COLORS.primary} />
              <Text style={styles.kycStepText}>Facial Verification</Text>
            </View>
          </View>
        </TouchableOpacity>
      </View>

      <TouchableOpacity
        style={styles.backButton}
        onPress={handleBack}
      >
        <Ionicons name="arrow-back" size={20} color={COLORS.primary} />
        <Text style={styles.backButtonText}>Back to Personal Details</Text>
      </TouchableOpacity>
    </View>
  );

  const renderStep = () => {
    switch (currentStep) {
      case 'screen1':
        return (
          <SignupScreen1
            data={signupData}
            updateData={updateSignupData}
            onNext={handleScreen1Submit}
            onBack={handleBack}
            isLoading={loading}
          />
        );
      case 'kyc_choice':
        return renderKYCChoice();
      case 'screen2':
      case 'screen3':
        // Import screens dynamically to avoid circular dependencies
        if (currentStep === 'screen2') {
          const SignupScreen2 = require('@/components/signup/SignupScreen2').default;
          return (
            <SignupScreen2
              data={signupData}
              updateData={updateSignupData}
              onNext={handleScreen2Submit}
              onBack={handleBack}
              isLoading={loading}
              authToken={authToken}
              userId={userId}
              isStandaloneKYC={false}
            />
          );
        } else {
          const SignupScreen3 = require('@/components/signup/SignupScreen3').default;
          return (
            <SignupScreen3
              data={signupData}
              updateData={updateSignupData}
              onNext={handleScreen3Submit}
              onBack={handleBack}
              isLoading={loading}
              authToken={authToken}
              userId={userId}
              isStandaloneKYC={false}
            />
          );
        }
      default:
        return null;
    }
  };

  if (initialLoading) {
    return (
      <SafeAreaView style={styles.container}>
        <View style={styles.loadingContainer}>
          <ActivityIndicator size="large" color={COLORS.primary} />
          <Text style={styles.loadingText}>Loading...</Text>
        </View>
      </SafeAreaView>
    );
  }

  // Don't show progress bar on KYC choice screen
  const showProgressBar = currentStep !== 'kyc_choice';

  return (
    <SafeAreaView style={styles.container}>
      {/* Progress Bar (only for actual steps) */}
      {showProgressBar && (
        <View style={styles.progressContainer}>
          <LinearProgress
            value={
              currentStep === 'screen1' ? 1 / 3 :
                currentStep === 'screen2' ? 2 / 3 :
                  currentStep === 'screen3' ? 3 / 3 : 0
            }
            color={COLORS.primary}
            variant="determinate"
            style={styles.progressBar}
            //@ts-ignore
            animation={{ duration: 300 }}
          />
          <View style={styles.stepIndicator}>
            <Text style={styles.stepText}>
              {currentStep === 'screen1' && 'Step 1 of 3 - Personal Details'}
              {currentStep === 'screen2' && 'Step 2 of 3 - Identity Verification'}
              {currentStep === 'screen3' && 'Step 3 of 3 - Facial Verification'}
            </Text>
          </View>
        </View>
      )}

      {/* Global Loading Overlay */}
      {loading && (
        <View style={styles.globalLoadingOverlay}>
          <View style={styles.loadingContent}>
            <ActivityIndicator size="large" color={COLORS.primary} />
            <Text style={styles.globalLoadingText}>
              {currentStep === 'screen1' && 'Creating your account...'}
              {currentStep === 'screen2' && 'Uploading documents...'}
              {currentStep === 'screen3' && 'Processing facial verification...'}
            </Text>
            <Text style={styles.loadingSubtext}>
              Please don't close the app
            </Text>
          </View>
        </View>
      )}

      {/* Current Step */}
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
    minWidth: 250,
  },
  globalLoadingText: {
    marginTop: 20,
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
    textAlign: 'center',
  },
  loadingSubtext: {
    marginTop: 8,
    fontSize: 14,
    color: COLORS.gray,
    textAlign: 'center',
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
  // KYC Choice Styles
  kycChoiceContainer: {
    flex: 1,
    padding: 20,
    backgroundColor: '#fff',
  },
  kycChoiceHeader: {
    alignItems: 'center',
    paddingVertical: 30,
  },
  kycChoiceTitle: {
    fontSize: 24,
    fontWeight: 'bold',
    color: COLORS.dark,
    marginTop: 20,
    textAlign: 'center',
  },
  kycChoiceSubtitle: {
    fontSize: 16,
    color: COLORS.gray,
    textAlign: 'center',
    marginTop: 10,
    lineHeight: 24,
    paddingHorizontal: 20,
  },
  kycChoiceOptions: {
    flex: 1,
    gap: 20,
    marginTop: 10,
  },
  kycOptionCard: {
    backgroundColor: '#fff',
    borderRadius: 16,
    padding: 24,
    borderWidth: 2,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.05,
    shadowRadius: 8,
    elevation: 3,
  },
  dashboardOption: {
    borderColor: COLORS.lightGray,
  },
  kycOption: {
    borderColor: COLORS.primary,
  },
  kycOptionIcon: {
    width: 60,
    height: 60,
    borderRadius: 30,
    backgroundColor: COLORS.lightPrimary,
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 16,
  },
  kycOptionTitle: {
    fontSize: 20,
    fontWeight: 'bold',
    color: COLORS.dark,
    marginBottom: 8,
  },
  kycOptionDescription: {
    fontSize: 14,
    color: COLORS.gray,
    lineHeight: 20,
    marginBottom: 16,
  },
  kycOptionNote: {
    fontSize: 12,
    color: COLORS.warning,
    fontStyle: 'italic',
  },
  kycSteps: {
    gap: 12,
    marginTop: 8,
  },
  kycStep: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 12,
  },
  kycStepText: {
    fontSize: 14,
    color: COLORS.dark,
    fontWeight: '500',
  },
  backButton: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    paddingVertical: 16,
    marginTop: 20,
  },
  backButtonText: {
    fontSize: 16,
    color: COLORS.primary,
    fontWeight: '600',
    marginLeft: 8,
  },
});

export default SignupFlow;