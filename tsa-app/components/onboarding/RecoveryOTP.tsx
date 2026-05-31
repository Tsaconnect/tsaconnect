// Recovery OTP entry + password reset (rendered by the recovery OTP route).
//
// Migrated onto the TSA Signature design system: legacy constants/theme styling
// (COLORS, StyleSheet, MaterialIcons, raw TextInput/TouchableOpacity) replaced
// with @/theme tokens + @/components/ui primitives. AUTH logic is preserved
// verbatim — the OTP verify step, resetPassword/forgotPassword API calls, resend
// logic, password validation, error→step routing, and navigation (router.replace
// to /login, router.back) are unchanged. The third-party OtpInput is retained but
// re-themed with token colors.
import React, { useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { OtpInput } from 'react-native-otp-entry';
import { router, useLocalSearchParams } from 'expo-router';
import api from '../services/api';
import { useTheme } from '@/theme';
import {
  Button,
  Card,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

type Step = 'otp' | 'password';

const RecoveryOTP = () => {
  const { colors, spacing, radius } = useTheme();
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ paddingHorizontal: spacing.sm, paddingTop: spacing.xs }}>
        <IconButton
          icon="chevronLeft"
          color={colors.accent}
          accessibilityLabel="Go back"
          onPress={() => {
            if (step === 'password') {
              setStep('otp');
            } else {
              router.back();
            }
          }}
        />
      </View>

      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        keyboardShouldPersistTaps="handled"
        showsVerticalScrollIndicator={false}
      >
        {/* Step indicator */}
        <View style={{ alignItems: 'center', marginBottom: spacing['3xl'] }}>
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              width: 120,
              justifyContent: 'center',
            }}
          >
            <View
              style={{
                width: 12,
                height: 12,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
              }}
            />
            <View
              style={{
                flex: 1,
                height: 2,
                marginHorizontal: spacing.xs,
                backgroundColor: step === 'password' ? colors.accent : colors.border,
              }}
            />
            <View
              style={{
                width: 12,
                height: 12,
                borderRadius: radius.pill,
                backgroundColor: step === 'password' ? colors.accent : colors.border,
              }}
            />
          </View>
          <View
            style={{
              flexDirection: 'row',
              justifyContent: 'space-between',
              width: 140,
              marginTop: spacing.xs,
            }}
          >
            <Text variant="caption" weight="600" color="accent">
              Verify
            </Text>
            <Text variant="caption" weight="600" color={step === 'password' ? 'accent' : 'textMuted'}>
              Reset
            </Text>
          </View>
        </View>

        {step === 'otp' ? (
          /* ───── Step 1: OTP Verification ───── */
          <View>
            <Text variant="title1" weight="700" color="accent">
              Verify Code
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm, marginBottom: spacing['2xl'] }}>
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
                containerStyle: { flexDirection: 'row', justifyContent: 'space-between' },
                inputsContainerStyle: { alignItems: 'center', justifyContent: 'center' },
                pinCodeContainerStyle: {
                  borderWidth: 1.5,
                  borderColor: errors.otp ? colors.danger : colors.border,
                  borderRadius: radius.md,
                  padding: spacing.md,
                  marginHorizontal: spacing.xs,
                  backgroundColor: colors.surface,
                },
                pinCodeTextStyle: {
                  fontSize: 18,
                  textAlign: 'center',
                  color: colors.textPrimary,
                },
                focusStickStyle: {
                  backgroundColor: colors.accent,
                  width: 2,
                  borderRadius: radius.sm,
                },
                focusedPinCodeContainerStyle: {
                  borderColor: colors.accent,
                },
              }}
            />
            {errors.otp ? (
              <Text variant="caption" color="danger" style={{ marginTop: spacing.xs, marginLeft: spacing.xs }}>
                {errors.otp}
              </Text>
            ) : null}

            <PressableScale
              onPress={handleResendCode}
              disabled={resending}
              style={{ alignSelf: 'center', marginTop: spacing.lg, padding: spacing.sm }}
            >
              {resending ? (
                <Spinner size="small" />
              ) : (
                <Text variant="bodySm" weight="600" color="accent">
                  Didn&apos;t receive a code? Resend
                </Text>
              )}
            </PressableScale>

            <Button
              title="Continue"
              size="lg"
              onPress={handleVerifyCode}
              disabled={otp.length < 6}
              style={{ marginTop: spacing['2xl'] }}
            />
          </View>
        ) : (
          /* ───── Step 2: New Password ───── */
          <View>
            <Text variant="title1" weight="700" color="accent">
              New Password
            </Text>
            <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.sm, marginBottom: spacing['2xl'] }}>
              Create a strong password for your account.
            </Text>

            <Input
              value={newPassword}
              placeholder="New Password (min. 8 characters)"
              secureTextEntry={!showPassword}
              editable={!loading}
              error={errors.password || undefined}
              onChangeText={(text) => {
                setNewPassword(text);
                if (errors.password) setErrors((prev) => ({ ...prev, password: undefined }));
              }}
              containerStyle={{ marginBottom: spacing.md }}
              leftAdornment={
                <Icon name="lock" size={20} color={errors.password ? colors.danger : colors.textMuted} />
              }
              rightAdornment={
                <IconButton
                  icon={showPassword ? 'eye' : 'eyeOff'}
                  size={20}
                  color={colors.textMuted}
                  accessibilityLabel={showPassword ? 'Hide password' : 'Show password'}
                  onPress={() => setShowPassword(!showPassword)}
                />
              }
            />

            <Input
              value={confirmPassword}
              placeholder="Confirm New Password"
              secureTextEntry={!showConfirmPassword}
              editable={!loading}
              error={errors.confirmPassword || undefined}
              onChangeText={(text) => {
                setConfirmPassword(text);
                if (errors.confirmPassword) setErrors((prev) => ({ ...prev, confirmPassword: undefined }));
              }}
              containerStyle={{ marginBottom: spacing.md }}
              leftAdornment={
                <Icon name="lock" size={20} color={errors.confirmPassword ? colors.danger : colors.textMuted} />
              }
              rightAdornment={
                <IconButton
                  icon={showConfirmPassword ? 'eye' : 'eyeOff'}
                  size={20}
                  color={colors.textMuted}
                  accessibilityLabel={showConfirmPassword ? 'Hide password' : 'Show password'}
                  onPress={() => setShowConfirmPassword(!showConfirmPassword)}
                />
              }
            />

            {/* Password requirements hint */}
            <Card padding="md" style={{ marginTop: spacing.xs }}>
              <Text variant="bodySm" weight="600" style={{ marginBottom: spacing.sm }}>
                Password must contain:
              </Text>
              <PasswordRule met={newPassword.length >= 8} label="At least 8 characters" />
              <PasswordRule met={/[A-Z]/.test(newPassword)} label="An uppercase letter" />
              <PasswordRule met={/[a-z]/.test(newPassword)} label="A lowercase letter" />
              <PasswordRule met={/\d/.test(newPassword)} label="A number" />
            </Card>

            <Button
              title="Reset Password"
              size="lg"
              onPress={handleResetPassword}
              loading={loading}
              disabled={loading}
              style={{ marginTop: spacing['2xl'] }}
            />
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

const PasswordRule = ({ met, label }: { met: boolean; label: string }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', marginBottom: spacing.xs }}>
      <Icon
        name={met ? 'checkCircle' : 'closeCircle'}
        size={16}
        color={met ? colors.success : colors.textDisabled}
      />
      <Text
        variant="bodySm"
        color={met ? 'textPrimary' : 'textMuted'}
        style={{ marginLeft: spacing.sm }}
      >
        {label}
      </Text>
    </View>
  );
};

export default RecoveryOTP;
