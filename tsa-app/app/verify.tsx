// app/verify.tsx
//
// The email code screen, reached two ways: at the end of signup
// (components/signup/signupflow, `?from=signup`) and from Profile. Only the end
// of signup hands off through goAfterAuth — a new customer who came from a pay
// link lands back on it; from Profile, Verify keeps its own landing.
import React from 'react';
import { SafeAreaView, StyleSheet } from 'react-native';
import Verify from '../components/onboarding/Verify';
import { router, useLocalSearchParams } from 'expo-router';
import { goAfterAuth } from '@/utils/postAuthRoute';

const VerifyScreen = () => {
  const { email, from } = useLocalSearchParams<{ email?: string; from?: string }>();
  const finishSignup = from === 'signup' ? () => { void goAfterAuth(router); } : undefined;

  return (
    <SafeAreaView style={styles.container}>
      <Verify email={email} showSkip={true} onSuccess={finishSignup} onSkip={finishSignup} />
    </SafeAreaView>
  );
};

export default VerifyScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
});
