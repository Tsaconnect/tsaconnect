// app/signup/index.tsx
import React, { useState } from 'react';
import { View, StyleSheet, Text } from 'react-native';
import { router } from 'expo-router';
import { LinearProgress } from 'react-native-elements';
import { COLORS, SIZES } from '@/constants/theme';
import SignupScreen1 from '@/components/signup/SignupScreen1';
import SignupScreen2 from '@/components/signup/SignupScreen2';
import SignupScreen3 from '@/components/signup/SignupScreen3';
import { useAuth } from '../../AuthContext/AuthContext';
import { SafeAreaView } from "react-native-safe-area-context"
export interface SignupData {
  // Screen 1 data
  name: string;
  username: string;
  country: string;
  state: string;
  city: string;
  address: string;
  phoneNumber: string;
  email: string;
  password: string;
  confirmPassword: string;
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
  const [currentStep, setCurrentStep] = useState(1);
  const [signupData, setSignupData] = useState<SignupData>({
    name: '',
    username: '',
    country: '',
    state: '',
    city: '',
    address: '',
    phoneNumber: '',
    email: '',
    password: '',
    confirmPassword: '',
    profilePhoto: null,
    referralCode: '',
  });
  const { loading, setLoading, signup } = useAuth();

  const updateSignupData = (data: Partial<SignupData>) => {
    setSignupData(prev => ({ ...prev, ...data }));
  };

  const handleNext = async () => {
    if (currentStep < 3) {
      setCurrentStep(currentStep + 1);
    } else {
      // Final submission
      await handleSubmit();
    }
  };

  const handleBack = () => {
    if (currentStep > 1) {
      setCurrentStep(currentStep - 1);
    } else {
      router.back();
    }
  };

  const handleSubmit = async () => {
    setLoading(true);
    try {
      // Combine all data into payload
      const payload = {
        ...signupData,
        phoneNumber: signupData.phoneNumber, // Format as needed
      };

      const result = await signup(payload);
      if (result.success) {
        router.push('/home');
      } else {
        alert(result.message);
      }
    } catch (err) {
      alert(err);
    } finally {
      setLoading(false);
    }
  };

  const renderStep = () => {
    switch (currentStep) {
      case 1:
        return (
          <SignupScreen1
            data={signupData}
            updateData={updateSignupData}
            onNext={handleNext}
            onBack={handleBack}
          />
        );
      case 2:
        return (
          <SignupScreen2
            data={signupData}
            updateData={updateSignupData}
            onNext={handleNext}
            onBack={handleBack}
          />
        );
      case 3:
        return (
          <SignupScreen3
            data={signupData}
            updateData={updateSignupData}
            onNext={handleNext}
            onBack={handleBack}
          />
        );
      default:
        return null;
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <View style={styles.progressContainer}>
        <LinearProgress
          value={currentStep / 3}
          color={COLORS.primary}
          variant="determinate"
          style={styles.progressBar}
        />
        <View style={styles.stepIndicator}>
          <Text style={styles.stepText}>Step {currentStep} of 3</Text>
        </View>
      </View>
      
      {loading && (
        <View style={styles.loadingOverlay}>
          <LinearProgress color={COLORS.primary} />
          <Text style={styles.loadingText}>Processing...</Text>
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
  },
  progressBar: {
    height: 4,
    borderRadius: 2,
  },
  stepIndicator: {
    alignItems: 'center',
    marginTop: 10,
  },
  stepText: {
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '600',
  },
  loadingOverlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(255,255,255,0.9)',
    justifyContent: 'center',
    alignItems: 'center',
    zIndex: 1000,
  },
  loadingText: {
    marginTop: 10,
    fontSize: 16,
    color: COLORS.primary,
  },
});

export default SignupFlow;