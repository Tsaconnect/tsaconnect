import React from 'react';
import { SafeAreaView, StyleSheet } from 'react-native';
import Verify from '../../components/onboarding/Verify';
import { useAuth } from '../../AuthContext/AuthContext';
import { router } from 'expo-router';

const VerifyEmailScreen = () => {
  const { currentUser, setEmailVerified } = useAuth();

  return (
    <SafeAreaView style={styles.container}>
      <Verify
        email={currentUser?.email}
        showSkip={false}
        onSuccess={() => {
          setEmailVerified(true);
          router.back();
        }}
      />
    </SafeAreaView>
  );
};

export default VerifyEmailScreen;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
});
