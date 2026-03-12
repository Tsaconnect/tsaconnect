import React, { useState } from 'react';
import {
  StyleSheet,
  Text,
  View,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
} from 'react-native';
import { OtpInput } from 'react-native-otp-entry';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';
import { router } from 'expo-router';

const Verify = ({ payLoad }: { payLoad?: any }) => {
  const [verificationCode, setVerificationCode] = useState('');
  const [loading, setLoading] = useState(false);
  const [resending, setResending] = useState(false);
  const [error, setError] = useState('');

  const email = payLoad?.email || 'your email';

  const handleVerify = async () => {
    if (verificationCode.length < 6) {
      setError('Please enter the full 6-digit code');
      return;
    }

    setError('');
    setLoading(true);
    try {
      // TODO: Call verify account API endpoint when available
      // await api.verifyAccount({ email: payLoad?.email, code: verificationCode });

      router.replace('/home');
    } catch (err: any) {
      Alert.alert('Verification Failed', err.message || 'Invalid code. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleResend = async () => {
    setResending(true);
    try {
      // TODO: Call resend verification code API
      Alert.alert('Code Sent', 'A new verification code has been sent.');
    } catch (err: any) {
      Alert.alert('Error', 'Failed to resend code. Please try again.');
    } finally {
      setResending(false);
    }
  };

  return (
    <View style={styles.container}>
      <TouchableOpacity onPress={() => router.back()} style={styles.backButton}>
        <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
      </TouchableOpacity>

      <View style={styles.content}>
        <Text style={styles.title}>Verify Your Account</Text>
        <Text style={styles.subtitle}>
          Enter the verification code sent to {email}
        </Text>

        <OtpInput
          numberOfDigits={6}
          onTextChange={(text) => {
            setVerificationCode(text);
            if (error) setError('');
          }}
          onFilled={(text) => setVerificationCode(text)}
          theme={{
            containerStyle: styles.otpContainer,
            inputsContainerStyle: styles.otpInputsContainer,
            pinCodeContainerStyle: {
              ...styles.pinCodeContainer,
              ...(error ? styles.pinCodeError : {}),
            },
            pinCodeTextStyle: styles.pinCodeText,
            focusStickStyle: styles.focusStick,
            focusedPinCodeContainerStyle: styles.activePinCodeContainer,
          }}
        />
        {error ? <Text style={styles.errorText}>{error}</Text> : null}

        <TouchableOpacity onPress={handleResend} disabled={resending} style={styles.resendButton}>
          {resending ? (
            <ActivityIndicator size="small" color={COLORS.primary} />
          ) : (
            <Text style={styles.resendText}>Didn't receive a code? Resend</Text>
          )}
        </TouchableOpacity>

        <TouchableOpacity
          style={[styles.button, loading && styles.buttonDisabled]}
          onPress={handleVerify}
          disabled={loading}
        >
          {loading ? (
            <ActivityIndicator size="small" color="#fff" />
          ) : (
            <Text style={styles.buttonText}>Verify</Text>
          )}
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default Verify;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
    padding: 20,
  },
  backButton: {
    marginTop: 40,
    padding: 8,
    alignSelf: 'flex-start',
  },
  content: {
    flex: 1,
    justifyContent: 'center',
  },
  title: {
    fontSize: 28,
    fontWeight: '700',
    color: COLORS.primary,
    marginBottom: 10,
  },
  subtitle: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 30,
    lineHeight: 20,
  },
  otpContainer: {
    flexDirection: 'row',
    justifyContent: 'space-between',
  },
  otpInputsContainer: {
    alignItems: 'center',
    justifyContent: 'center',
  },
  pinCodeContainer: {
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    borderRadius: 10,
    padding: 10,
    marginHorizontal: 4,
  },
  pinCodeError: {
    borderColor: COLORS.danger,
  },
  pinCodeText: {
    fontSize: 18,
    textAlign: 'center',
    color: COLORS.dark,
  },
  focusStick: {
    backgroundColor: COLORS.primary,
    width: 2,
    borderRadius: 5,
  },
  activePinCodeContainer: {
    borderColor: COLORS.primary,
  },
  resendButton: {
    alignSelf: 'center',
    marginTop: 16,
    marginBottom: 8,
    padding: 8,
  },
  resendText: {
    color: COLORS.primary,
    fontSize: 14,
    fontWeight: '500',
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 12,
    marginLeft: 15,
    marginTop: 8,
  },
  button: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    height: 56,
    justifyContent: 'center',
    alignItems: 'center',
    marginTop: 24,
    shadowColor: COLORS.primary,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 5,
  },
  buttonDisabled: {
    backgroundColor: COLORS.lightGray,
    opacity: 0.7,
  },
  buttonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
  },
});
