// components/onboarding/Verify.tsx
//
// Account / email verification screen — enter the 6-digit OTP, verify, resend
// (with cooldown), or skip. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives).
//
// AUTH screen: the verify API call (api.verifyOtp), resend (api.resendOtp),
// setEmailVerified, the cooldown timer, and all navigation (router.back /
// router.replace('/home') / onSkip / onSuccess) are preserved verbatim — this is
// a visual/structural restyle only. The OtpInput from react-native-otp-entry is
// reused unchanged; only its theme is re-tokenized.
import React, { useState, useEffect, useCallback } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { OtpInput } from 'react-native-otp-entry';
import { router } from 'expo-router';
import api from '../services/api';
import { useAuth } from '../../AuthContext/AuthContext';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Icon,
  IconButton,
  PressableScale,
  Spinner,
  Text,
} from '@/components/ui';

interface VerifyProps {
  email?: string;
  onSkip?: () => void;
  onSuccess?: () => void;
  showSkip?: boolean;
}

const Verify = ({ email, onSkip, onSuccess, showSkip = true }: VerifyProps) => {
  const { colors, spacing, radius } = useTheme();
  const { setEmailVerified } = useAuth();
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
        setEmailVerified(true);
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ flexGrow: 1, padding: spacing.lg }}
        keyboardShouldPersistTaps="handled"
      >
        <IconButton
          icon="chevronLeft"
          onPress={() => router.back()}
          accessibilityLabel="Go back"
          style={{ marginStart: -spacing.sm }}
        />

        <View style={{ flex: 1, justifyContent: 'center', paddingVertical: spacing['2xl'] }}>
          <View
            style={{
              width: 56,
              height: 56,
              borderRadius: radius.pill,
              alignItems: 'center',
              justifyContent: 'center',
              backgroundColor: colors.accentMuted,
              marginBottom: spacing.lg,
            }}
          >
            <Icon name="verified" size={28} color={colors.accent} />
          </View>

          <Text variant="title1" weight="700">
            Verify Your Email
          </Text>
          <Text
            variant="bodySm"
            color="textSecondary"
            style={{ marginTop: spacing.sm, marginBottom: spacing['2xl'] }}
          >
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
              containerStyle: {
                flexDirection: 'row',
                justifyContent: 'space-between',
              },
              inputsContainerStyle: {
                alignItems: 'center',
                justifyContent: 'center',
              },
              pinCodeContainerStyle: {
                borderWidth: 1.5,
                borderColor: error ? colors.danger : colors.border,
                borderRadius: radius.md,
                backgroundColor: colors.surface,
                width: 48,
                height: 56,
                marginHorizontal: spacing.xs,
              },
              pinCodeTextStyle: {
                fontSize: 20,
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

          {error ? (
            <AccentCard
              padding="md"
              railColor={colors.danger}
              style={{ marginTop: spacing.lg }}
            >
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="warning" size={18} color={colors.danger} />
                <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                  {error}
                </Text>
              </View>
            </AccentCard>
          ) : null}

          {successMsg ? (
            <AccentCard
              padding="md"
              railColor={colors.success}
              style={{ marginTop: spacing.lg }}
            >
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Icon name="checkCircle" size={18} color={colors.success} />
                <Text variant="bodySm" color="success" style={{ flex: 1 }}>
                  {successMsg}
                </Text>
              </View>
            </AccentCard>
          ) : null}

          <View style={{ alignItems: 'center', marginTop: spacing.lg, marginBottom: spacing.sm }}>
            <PressableScale
              onPress={handleResend}
              disabled={resending || cooldown > 0}
              accessibilityRole="button"
              accessibilityLabel="Resend verification code"
              accessibilityState={{ disabled: resending || cooldown > 0 }}
              style={{ padding: spacing.sm }}
            >
              {resending ? (
                <Spinner size="small" />
              ) : cooldown > 0 ? (
                <Text variant="bodySm" color="textMuted">
                  Resend code in {cooldown}s
                </Text>
              ) : (
                <Text variant="bodySm" weight="600" color="accent">
                  Didn&apos;t receive a code? Resend
                </Text>
              )}
            </PressableScale>
          </View>

          <Button
            title="Verify"
            onPress={handleVerify}
            loading={loading}
            disabled={loading}
            size="lg"
            style={{ marginTop: spacing.lg }}
          />

          {showSkip && (
            <View style={{ alignItems: 'center', marginTop: spacing.lg }}>
              <PressableScale
                onPress={handleSkip}
                accessibilityRole="button"
                accessibilityLabel="Skip verification for now"
                style={{ padding: spacing.sm }}
              >
                <Text variant="bodySm" color="textSecondary">
                  Skip for now
                </Text>
              </PressableScale>
            </View>
          )}
        </View>
      </ScrollView>
    </SafeAreaView>
  );
};

export default Verify;
