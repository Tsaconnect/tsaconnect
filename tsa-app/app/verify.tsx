import React from 'react';
import { SafeAreaView, StyleSheet } from 'react-native';
import Verify from '../components/onboarding/Verify';
import { useLocalSearchParams } from 'expo-router';

const VerifyScreen = () => {
  const { email } = useLocalSearchParams<{ email?: string }>();

  return (
    <SafeAreaView style={styles.container}>
      <Verify email={email} showSkip={true} />
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
