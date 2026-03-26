import React, { useState } from 'react';
import {
  View,
  StyleSheet,
  Text,
  Alert,
  ActivityIndicator,
} from 'react-native';
import { router } from 'expo-router';
import { COLORS } from '@/constants/theme';
import SignupScreen1 from '@/components/signup/SignupScreen1';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { api } from '../services/api';
import { SafeAreaView } from "react-native-safe-area-context";
import { SignupData } from './signup';

const SignupFlow = () => {
  const [signupData, setSignupData] = useState<SignupData>({
    name: '',
    username: '',
    email: '',
    phoneNumber: '',
    password: '',
    confirmPassword: '',
    address: '',
    referralCode: '',
  });
  const [loading, setLoading] = useState(false);

  const updateSignupData = (data: Partial<SignupData>) => {
    setSignupData(prev => ({ ...prev, ...data }));
  };

  const handleSubmit = async () => {
    setLoading(true);
    try {
      const signupPayload = {
        name: signupData.name.trim(),
        username: signupData.username.trim().toLowerCase(),
        email: signupData.email.trim().toLowerCase(),
        password: signupData.password,
        confirmPassword: signupData.confirmPassword,
        phoneNumber: signupData.phoneNumber.trim(),
        address: signupData.address.trim(),
        referralCode: signupData.referralCode?.trim() || undefined,
      };

      //@ts-ignore
      const result = await api.signup(signupPayload);

      if (result.success) {
        //@ts-ignore
        const token = result.data.token;
        //@ts-ignore
        const userId = result.data.userId;

        await AsyncStorage.setItem('authToken', token);
        await AsyncStorage.setItem('userId', userId);
        api.setToken(token);

        router.replace({
          pathname: '/verify',
          params: { email: signupData.email.trim().toLowerCase() },
        });
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

  return (
    <SafeAreaView style={styles.container}>
      {loading && (
        <View style={styles.loadingOverlay}>
          <View style={styles.loadingContent}>
            <ActivityIndicator size="large" color={COLORS.primary} />
            <Text style={styles.loadingText}>Creating your account...</Text>
            <Text style={styles.loadingSubtext}>Please don't close the app</Text>
          </View>
        </View>
      )}

      <SignupScreen1
        data={signupData}
        updateData={updateSignupData}
        onNext={handleSubmit}
        onBack={() => router.back()}
        isLoading={loading}
      />
    </SafeAreaView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  loadingOverlay: {
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
  loadingText: {
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
});

export default SignupFlow;
