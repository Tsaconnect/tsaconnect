import React, { useState } from 'react';
import {
  StyleSheet,
  Text,
  View,
  TextInput,
  TouchableOpacity,
  ActivityIndicator,
  ScrollView,
  Alert,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { OtpInput } from 'react-native-otp-entry';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';
import { router, useLocalSearchParams } from 'expo-router';
import api from '../services/api';

type Step = 'otp' | 'password';

const RecoveryOTP = () => {
  const { emailOrPhone } = useLocalSearchParams<{ emailOrPhone?: string }>();
  const [step, setStep] = useState<Step>('otp');
  const [otp, setOtp] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [resending, setResending] = useState(false);
  const [errors, setErrors] = useState<{ otp?: string; password?: string; confirmPassword?: string }>({});

  const handleVerifyCode = () => {
    if (otp.length < 6) {
      setErrors({ otp: 'Please enter the full 6-digit code' });
      return;
    }
    setErrors({});
    setStep('password');
  };

  const validatePasswords = (): boolean => {
    const newErrors: typeof errors = {};

    if (!newPassword) {
      newErrors.password = 'New password is required';
    } else if (newPassword.length < 8) {
      newErrors.password = 'Must be at least 8 characters';
    } else if (!(/[A-Z]/.test(newPassword) && /[a-z]/.test(newPassword) && /\d/.test(newPassword))) {
      newErrors.password = 'Include uppercase, lowercase, and a number';
    }
    if (!confirmPassword) {
      newErrors.confirmPassword = 'Please confirm your password';
    } else if (newPassword !== confirmPassword) {
      newErrors.confirmPassword = 'Passwords do not match';
    }

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleResetPassword = async () => {
    if (!validatePasswords()) return;

    setLoading(true);
    try {
      const result = await api.resetPassword(emailOrPhone || '', otp, newPassword);
      if (result.success) {
        Alert.alert(
          'Password Reset',
          'Your password has been updated successfully.',
          [{ text: 'Login', onPress: () => router.replace('/login') }]
        );
      } else {
        const msg = (result.message || '').toLowerCase();
        // If the OTP was invalid, go back to OTP step
        if (msg.includes('code') || msg.includes('otp') || msg.includes('expired')) {
          setStep('otp');
          setErrors({ otp: result.message || 'Invalid code. Please try again.' });
        } else {
          Alert.alert('Error', result.message || 'Failed to reset password. Please try again.');
        }
      }
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to reset password. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleResendCode = async () => {
    setResending(true);
    try {
      const result = await api.forgotPassword(emailOrPhone || '');
      if (result.success) {
        setOtp('');
        setStep('otp');
        Alert.alert('Code Sent', 'A new verification code has been sent.');
      } else {
        Alert.alert('Error', result.message || 'Failed to resend code.');
      }
    } catch (err: any) {
      Alert.alert('Error', 'Failed to resend code. Please try again.');
    } finally {
      setResending(false);
    }
  };

  return (
    <SafeAreaView style={styles.container} edges={['top']}>
    <ScrollView style={styles.scrollView} contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
      <TouchableOpacity
        onPress={() => {
          if (step === 'password') {
            setStep('otp');
          } else {
            router.back();
          }
        }}
        style={styles.backButton}
      >
        <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
      </TouchableOpacity>

      {/* Step indicator */}
      <View style={styles.stepIndicator}>
        <View style={styles.stepRow}>
          <View style={[styles.stepDot, styles.stepDotActive]} />
          <View style={[styles.stepLine, step === 'password' && styles.stepLineActive]} />
          <View style={[styles.stepDot, step === 'password' && styles.stepDotActive]} />
        </View>
        <View style={styles.stepLabelRow}>
          <Text style={[styles.stepLabel, styles.stepLabelActive]}>Verify</Text>
          <Text style={[styles.stepLabel, step === 'password' && styles.stepLabelActive]}>Reset</Text>
        </View>
      </View>

      {step === 'otp' ? (
        /* ───── Step 1: OTP Verification ───── */
        <View style={styles.stepContainer}>
          <Text style={styles.title}>Verify Code</Text>
          <Text style={styles.subtitle}>
            {emailOrPhone
              ? `Enter the 6-digit code sent to ${emailOrPhone}`
              : 'Enter the code sent to your email'}
          </Text>

          <OtpInput
            numberOfDigits={6}
            onTextChange={(text) => {
              setOtp(text);
              if (errors.otp) setErrors({});
            }}
            onFilled={(text) => setOtp(text)}
            theme={{
              containerStyle: styles.otpContainer,
              inputsContainerStyle: styles.otpInputsContainer,
              pinCodeContainerStyle: {
                ...styles.pinCodeContainer,
                ...(errors.otp ? styles.pinCodeError : {}),
              },
              pinCodeTextStyle: styles.pinCodeText,
              focusStickStyle: styles.focusStick,
              focusedPinCodeContainerStyle: styles.activePinCodeContainer,
            }}
          />
          {errors.otp ? <Text style={styles.errorText}>{errors.otp}</Text> : null}

          <TouchableOpacity onPress={handleResendCode} disabled={resending} style={styles.resendButton}>
            {resending ? (
              <ActivityIndicator size="small" color={COLORS.primary} />
            ) : (
              <Text style={styles.resendText}>Didn't receive a code? Resend</Text>
            )}
          </TouchableOpacity>

          <TouchableOpacity
            style={[styles.button, otp.length < 6 && styles.buttonDisabled]}
            onPress={handleVerifyCode}
            disabled={otp.length < 6}
          >
            <Text style={styles.buttonText}>Continue</Text>
          </TouchableOpacity>
        </View>
      ) : (
        /* ───── Step 2: New Password ───── */
        <View style={styles.stepContainer}>
          <Text style={styles.title}>New Password</Text>
          <Text style={styles.subtitle}>
            Create a strong password for your account.
          </Text>

          <View style={[styles.inputContainer, errors.password ? styles.inputError : null]}>
            <MaterialIcons name="lock" size={20} color={errors.password ? COLORS.danger : COLORS.gray} style={styles.inputIcon} />
            <TextInput
              style={styles.input}
              placeholder="New Password (min. 8 characters)"
              placeholderTextColor="#999"
              value={newPassword}
              onChangeText={(text) => {
                setNewPassword(text);
                if (errors.password) setErrors(prev => ({ ...prev, password: undefined }));
              }}
              secureTextEntry={!showPassword}
              editable={!loading}
            />
            <TouchableOpacity onPress={() => setShowPassword(!showPassword)} style={styles.passwordIcon}>
              <MaterialIcons name={showPassword ? 'visibility' : 'visibility-off'} size={20} color={COLORS.gray} />
            </TouchableOpacity>
          </View>
          {errors.password ? <Text style={styles.errorText}>{errors.password}</Text> : null}

          <View style={[styles.inputContainer, errors.confirmPassword ? styles.inputError : null]}>
            <MaterialIcons name="lock-outline" size={20} color={errors.confirmPassword ? COLORS.danger : COLORS.gray} style={styles.inputIcon} />
            <TextInput
              style={styles.input}
              placeholder="Confirm New Password"
              placeholderTextColor="#999"
              value={confirmPassword}
              onChangeText={(text) => {
                setConfirmPassword(text);
                if (errors.confirmPassword) setErrors(prev => ({ ...prev, confirmPassword: undefined }));
              }}
              secureTextEntry={!showConfirmPassword}
              editable={!loading}
            />
            <TouchableOpacity onPress={() => setShowConfirmPassword(!showConfirmPassword)} style={styles.passwordIcon}>
              <MaterialIcons name={showConfirmPassword ? 'visibility' : 'visibility-off'} size={20} color={COLORS.gray} />
            </TouchableOpacity>
          </View>
          {errors.confirmPassword ? <Text style={styles.errorText}>{errors.confirmPassword}</Text> : null}

          {/* Password requirements hint */}
          <View style={styles.requirementsBox}>
            <Text style={styles.requirementsTitle}>Password must contain:</Text>
            <PasswordRule met={newPassword.length >= 8} label="At least 8 characters" />
            <PasswordRule met={/[A-Z]/.test(newPassword)} label="An uppercase letter" />
            <PasswordRule met={/[a-z]/.test(newPassword)} label="A lowercase letter" />
            <PasswordRule met={/\d/.test(newPassword)} label="A number" />
          </View>

          <TouchableOpacity
            style={[styles.button, loading && styles.buttonDisabled]}
            onPress={handleResetPassword}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.buttonText}>Reset Password</Text>
            )}
          </TouchableOpacity>
        </View>
      )}
    </ScrollView>
    </SafeAreaView>
  );
};

const PasswordRule = ({ met, label }: { met: boolean; label: string }) => (
  <View style={styles.ruleRow}>
    <MaterialIcons
      name={met ? 'check-circle' : 'radio-button-unchecked'}
      size={16}
      color={met ? '#10b981' : '#ccc'}
    />
    <Text style={[styles.ruleText, met && styles.ruleTextMet]}>{label}</Text>
  </View>
);

export default RecoveryOTP;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  scrollView: {
    flex: 1,
  },
  content: {
    padding: 20,
    paddingBottom: 40,
  },
  backButton: {
    padding: 8,
    alignSelf: 'flex-start',
    marginBottom: 8,
  },
  // Step indicator
  stepIndicator: {
    marginBottom: 32,
    alignItems: 'center',
  },
  stepRow: {
    flexDirection: 'row',
    alignItems: 'center',
    width: 120,
    justifyContent: 'center',
  },
  stepDot: {
    width: 12,
    height: 12,
    borderRadius: 6,
    backgroundColor: '#e5e7eb',
  },
  stepDotActive: {
    backgroundColor: COLORS.primary,
  },
  stepLine: {
    flex: 1,
    height: 2,
    backgroundColor: '#e5e7eb',
    marginHorizontal: 4,
  },
  stepLineActive: {
    backgroundColor: COLORS.primary,
  },
  stepLabelRow: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    width: 140,
    marginTop: 6,
  },
  stepLabel: {
    fontSize: 12,
    color: '#ccc',
    fontWeight: '500',
  },
  stepLabelActive: {
    color: COLORS.primary,
  },
  // Shared
  stepContainer: {
    flex: 1,
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
    marginBottom: 24,
    lineHeight: 20,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 12,
    marginLeft: 4,
    marginTop: 4,
    marginBottom: 8,
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
    opacity: 0.5,
  },
  buttonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
  },
  // OTP step
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
    padding: 8,
  },
  resendText: {
    color: COLORS.primary,
    fontSize: 14,
    fontWeight: '500',
  },
  // Password step
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    paddingHorizontal: 15,
    marginBottom: 4,
  },
  inputError: {
    borderColor: COLORS.danger,
    borderWidth: 1.5,
    backgroundColor: '#fff5f5',
  },
  inputIcon: {
    marginRight: 10,
  },
  input: {
    flex: 1,
    height: 56,
    fontSize: 16,
    color: COLORS.dark,
  },
  passwordIcon: {
    padding: 10,
  },
  // Password requirements
  requirementsBox: {
    backgroundColor: '#f9fafb',
    borderRadius: 10,
    padding: 14,
    marginTop: 16,
  },
  requirementsTitle: {
    fontSize: 13,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 8,
  },
  ruleRow: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 4,
  },
  ruleText: {
    fontSize: 13,
    color: '#999',
    marginLeft: 8,
  },
  ruleTextMet: {
    color: COLORS.dark,
  },
});
