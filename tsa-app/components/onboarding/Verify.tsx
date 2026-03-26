import React, { useState, useEffect, useCallback } from 'react';
import {
  StyleSheet,
  Text,
  View,
  TouchableOpacity,
  ActivityIndicator,
} from 'react-native';
import { OtpInput } from 'react-native-otp-entry';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';
import { router } from 'expo-router';
import api from '../services/api';

interface VerifyProps {
  email?: string;
  onSkip?: () => void;
  onSuccess?: () => void;
  showSkip?: boolean;
}

const Verify = ({ email, onSkip, onSuccess, showSkip = true }: VerifyProps) => {
  const [verificationCode, setVerificationCode] = useState('');
  const [loading, setLoading] = useState(false);
  const [resending, setResending] = useState(false);
  const [error, setError] = useState('');
  const [successMsg, setSuccessMsg] = useState('');
  const [cooldown, setCooldown] = useState(0);

  useEffect(() => {
    if (cooldown <= 0) return;
    const timer = setTimeout(() => setCooldown(cooldown - 1), 1000);
    return () => clearTimeout(timer);
  }, [cooldown]);

  const handleVerify = useCallback(async () => {
    if (verificationCode.length < 6) {
      setError('Please enter the full 6-digit code');
      return;
    }

    setError('');
    setSuccessMsg('');
    setLoading(true);
    try {
      const result = await api.verifyOtp(verificationCode);
      if (result.success) {
        setSuccessMsg('Email verified successfully!');
        if (onSuccess) {
          onSuccess();
        } else {
          router.replace('/home');
        }
      } else {
        setError(result.message || 'Invalid code. Please try again.');
      }
    } catch (err: any) {
      setError(err.message || 'Verification failed. Please try again.');
    } finally {
      setLoading(false);
    }
  }, [verificationCode, onSuccess]);

  const handleResend = useCallback(async () => {
    if (cooldown > 0) return;

    setResending(true);
    setError('');
    setSuccessMsg('');
    try {
      const result = await api.resendOtp();
      if (result.success) {
        setSuccessMsg('A new code has been sent to your email.');
        setCooldown(60);
        setVerificationCode('');
      } else {
        setError(result.message || 'Failed to resend code.');
      }
    } catch (err: any) {
      setError('Failed to resend code. Please try again.');
    } finally {
      setResending(false);
    }
  }, [cooldown]);

  const handleSkip = useCallback(() => {
    if (onSkip) {
      onSkip();
    } else {
      router.replace('/home');
    }
  }, [onSkip]);

  const displayEmail = email || 'your email';

  return (
    <View style={styles.container}>
      <TouchableOpacity onPress={() => router.back()} style={styles.backButton}>
        <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
      </TouchableOpacity>

      <View style={styles.content}>
        <Text style={styles.title}>Verify Your Email</Text>
        <Text style={styles.subtitle}>
          Enter the 6-digit code sent to {displayEmail}
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
        {successMsg ? <Text style={styles.successText}>{successMsg}</Text> : null}

        <TouchableOpacity
          onPress={handleResend}
          disabled={resending || cooldown > 0}
          style={styles.resendButton}
        >
          {resending ? (
            <ActivityIndicator size="small" color={COLORS.primary} />
          ) : cooldown > 0 ? (
            <Text style={styles.resendDisabledText}>
              Resend code in {cooldown}s
            </Text>
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

        {showSkip && (
          <TouchableOpacity onPress={handleSkip} style={styles.skipButton}>
            <Text style={styles.skipText}>Skip for now</Text>
          </TouchableOpacity>
        )}
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
  resendDisabledText: {
    color: COLORS.gray,
    fontSize: 14,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 13,
    marginLeft: 4,
    marginTop: 8,
  },
  successText: {
    color: COLORS.success,
    fontSize: 13,
    marginLeft: 4,
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
  skipButton: {
    alignSelf: 'center',
    marginTop: 16,
    padding: 8,
  },
  skipText: {
    color: COLORS.gray,
    fontSize: 14,
  },
});
