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
import { OtpInput } from 'react-native-otp-entry';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';
import { router, useLocalSearchParams } from 'expo-router';

const RecoveryOTP = () => {
  const { emailOrPhone } = useLocalSearchParams<{ emailOrPhone?: string }>();
  const [otp, setOtp] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const [confirmPassword, setConfirmPassword] = useState('');
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [loading, setLoading] = useState(false);
  const [resending, setResending] = useState(false);
  const [errors, setErrors] = useState<{ otp?: string; password?: string; confirmPassword?: string }>({});

  const validate = (): boolean => {
    const newErrors: typeof errors = {};

    if (otp.length < 6) {
      newErrors.otp = 'Please enter the full 6-digit code';
    }
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
    if (!validate()) return;

    setLoading(true);
    try {
      // TODO: Call password reset API endpoint when available
      // await api.resetPassword({ emailOrPhone, otp, newPassword });

      Alert.alert(
        'Password Reset',
        'Your password has been updated successfully.',
        [{ text: 'Login', onPress: () => router.replace('/login') }]
      );
    } catch (err: any) {
      Alert.alert('Error', err.message || 'Failed to reset password. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleResendCode = async () => {
    setResending(true);
    try {
      // TODO: Call resend OTP API endpoint when available
      // await api.requestPasswordReset(emailOrPhone);
      Alert.alert('Code Sent', 'A new verification code has been sent.');
    } catch (err: any) {
      Alert.alert('Error', 'Failed to resend code. Please try again.');
    } finally {
      setResending(false);
    }
  };

  return (
    <ScrollView style={styles.container} contentContainerStyle={styles.content} showsVerticalScrollIndicator={false}>
      <TouchableOpacity onPress={() => router.back()} style={styles.backButton}>
        <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
      </TouchableOpacity>

      <Text style={styles.title}>Verify & Reset</Text>
      <Text style={styles.subtitle}>
        {emailOrPhone
          ? `Enter the code sent to ${emailOrPhone}`
          : 'Kindly enter the code sent to your email or phone'}
      </Text>

      <OtpInput
        numberOfDigits={6}
        onTextChange={(text) => {
          setOtp(text);
          if (errors.otp) setErrors(prev => ({ ...prev, otp: undefined }));
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

      <View style={styles.passwordSection}>
        <Text style={styles.sectionLabel}>Set New Password</Text>

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
    </ScrollView>
  );
};

export default RecoveryOTP;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  content: {
    padding: 20,
    paddingTop: 80,
  },
  backButton: {
    position: 'absolute',
    top: 0,
    left: 0,
    padding: 8,
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
  passwordSection: {
    marginTop: 24,
  },
  sectionLabel: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 12,
  },
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
  errorText: {
    color: COLORS.danger,
    fontSize: 12,
    marginLeft: 15,
    marginBottom: 8,
  },
  button: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    height: 56,
    justifyContent: 'center',
    alignItems: 'center',
    marginTop: 24,
    marginBottom: 30,
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
