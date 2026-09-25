// components/signup/signupflow.tsx
//
// Multi-step signup flow (rendered by app/signup.tsx). Holds the signup form
// state machine, drives the signup API call, persists the auth token, and routes
// to /verify on success. Restyled onto the TSA Signature design system — tokens +
// primitives only; the step state, validation (in SignupScreen1), API call, and
// navigation are preserved verbatim.
import React, { useState } from 'react';
import { Alert, View } from 'react-native';
import { router } from 'expo-router';
import SignupScreen1 from '@/components/signup/SignupScreen1';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { api } from '../services/api';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Card, Spinner, Text } from '@/components/ui';
import { SignupData, createEmptySignupData } from './signup';

const SignupFlow = () => {
  const { colors, spacing, radius } = useTheme();
  const [signupData, setSignupData] = useState<SignupData>(createEmptySignupData);
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
        referralCode: signupData.referralCode?.trim().toLowerCase() || undefined,
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      {loading && (
        <View
          style={{
            position: 'absolute',
            top: 0,
            left: 0,
            right: 0,
            bottom: 0,
            backgroundColor: colors.overlay,
            justifyContent: 'center',
            alignItems: 'center',
            zIndex: 1000,
          }}
        >
          <Card
            elevation="lg"
            style={{
              alignItems: 'center',
              padding: spacing['3xl'],
              borderRadius: radius.lg,
              minWidth: 250,
            }}
          >
            <Spinner size="large" label="Creating your account..." />
            <Text variant="bodySm" color="textMuted" align="center" style={{ marginTop: spacing.sm }}>
              Please don&apos;t close the app
            </Text>
          </Card>
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

export default SignupFlow;
