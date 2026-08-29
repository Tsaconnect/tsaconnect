// Password recovery — request reset (rendered by the recovery route).
//
// Migrated onto the TSA Signature design system: legacy constants/theme styling
// replaced with @/theme tokens + @/components/ui primitives. AUTH logic
// (forgotPassword API call, validation, error handling, and navigation to the
// OTP / login / signup routes) is preserved verbatim.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { goBack } from '@/utils/back';
import api from '../services/api';
import { useTheme } from '@/theme';
import {
  Button,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Text,
} from '@/components/ui';

export default function PasswordRecovery() {
  const { colors, spacing, radius } = useTheme();
  const [emailOrPhone, setEmailOrPhone] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);

  const handleSendCode = async () => {
    if (!emailOrPhone.trim()) {
      setError('Please enter your email address');
      return;
    }

    setError('');
    setLoading(true);

    try {
      const result = await api.forgotPassword(emailOrPhone.trim());
      if (!result.success) {
        setError(result.message || 'Failed to send recovery code. Please try again.');
        return;
      }

      // Navigate to OTP screen with email context
      router.push(`/passwordOTP?emailOrPhone=${encodeURIComponent(emailOrPhone.trim())}`);
    } catch (err: any) {
      setError(err.message || 'Failed to send recovery code. Please try again.');
    } finally {
      setLoading(false);
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ paddingHorizontal: spacing.sm, paddingTop: spacing.xs }}>
        <IconButton
          icon="chevronLeft"
          onPress={() => goBack('/login')}
          color={colors.accent}
          accessibilityLabel="Go back"
        />
      </View>
      <ScrollView
        contentContainerStyle={{
          flexGrow: 1,
          justifyContent: 'center',
          padding: spacing.lg,
        }}
        keyboardShouldPersistTaps="handled"
      >
        <View style={{ width: '100%', maxWidth: 400, alignSelf: 'center' }}>
          <View style={{ alignItems: 'center', marginBottom: spacing['3xl'] }}>
            <View
              style={{
                width: 72,
                height: 72,
                borderRadius: radius.pill,
                backgroundColor: colors.accentMuted,
                alignItems: 'center',
                justifyContent: 'center',
                marginBottom: spacing.lg,
              }}
            >
              <Icon name="lock" size={32} color={colors.accent} />
            </View>
            <Text variant="title1" weight="700" align="center">
              Recover Password
            </Text>
            <Text
              variant="bodySm"
              color="textSecondary"
              align="center"
              style={{ marginTop: spacing.sm }}
            >
              Enter the email associated with your account and we&apos;ll send you a verification code.
            </Text>
          </View>

          <View>
            <Input
              value={emailOrPhone}
              onChangeText={(text) => {
                setEmailOrPhone(text);
                if (error) setError('');
              }}
              placeholder="Email Address"
              keyboardType="email-address"
              autoCapitalize="none"
              autoComplete="email"
              editable={!loading}
              error={error || undefined}
              containerStyle={{ marginBottom: spacing.lg }}
              leftAdornment={
                <Icon
                  name="mail"
                  size={20}
                  color={error ? colors.danger : colors.textMuted}
                />
              }
            />

            <Button
              title="Send Code"
              onPress={handleSendCode}
              loading={loading}
              disabled={loading}
              size="lg"
            />

            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'center',
                alignItems: 'center',
                gap: spacing.sm,
                marginTop: spacing['2xl'],
              }}
            >
              <Text variant="bodySm" color="textSecondary">
                Remember your password?
              </Text>
              <PressableScale onPress={() => router.push('/login')} disabled={loading}>
                <Text
                  variant="bodySm"
                  weight="600"
                  color={loading ? 'textDisabled' : 'accent'}
                >
                  Login
                </Text>
              </PressableScale>
            </View>

            <View
              style={{
                flexDirection: 'row',
                justifyContent: 'center',
                alignItems: 'center',
                gap: spacing.sm,
                marginTop: spacing.lg,
              }}
            >
              <Text variant="bodySm" color="textSecondary">
                New to TSA?
              </Text>
              <PressableScale onPress={() => router.push('/signup')} disabled={loading}>
                <Text
                  variant="bodySm"
                  weight="600"
                  color={loading ? 'textDisabled' : 'accent'}
                >
                  Create Account
                </Text>
              </PressableScale>
            </View>
          </View>
        </View>
      </ScrollView>
    </SafeAreaView>
  );
}
