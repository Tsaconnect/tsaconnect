// app/merchants/activate-pm.tsx
//
// Payment-Merchant activation screen. After an admin APPROVES a PM application,
// the merchant completes onboarding here by supplying their BVN or NIN. This is
// required by the licensed fiat provider to mint the NGN virtual account for
// NGN static accounts.
//
// PRIVACY: the BVN/NIN is sent straight to the backend, which forwards it to the
// provider and never stores it. TSA does not persist the id number anywhere —
// it only transits this one request. The copy below states that to the user.
//
// Flow: pick idType (BVN|NIN) → enter 11-digit number → submit → on success the
// PM goes live and we return to the dashboard.
//
// Under central inbound (GET /p2p/pm/me → activationNeedsId: false) buyers pay
// TSA Connect's account and nothing is minted, so the screen asks for no id and
// activation is a single confirm. The server then requires the merchant's own
// identity verification instead, refused with a KYC code the shared prompt
// handles.

import React, { useMemo, useState } from 'react';
import { View, TextInput, ScrollView, Alert, KeyboardAvoidingView, Platform } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Chip, Icon, Spinner, Text } from '@/components/ui';
import { useIsPaymentMerchant } from '@/hooks/useIsPaymentMerchant';
import { activatePmAccount, type PmIdType } from '@/services/p2p';
import { promptKycRefusal } from '@/utils/kycRefusal';

const ID_TYPES: { label: string; value: PmIdType }[] = [
  { label: 'BVN', value: 'BVN' },
  { label: 'NIN', value: 'NIN' },
];

// Both a Nigerian BVN and NIN are exactly 11 digits.
const ID_NUMBER_RE = /^\d{11}$/;

export function isValidPmIdNumber(v: string): boolean {
  return ID_NUMBER_RE.test(v.trim());
}

type ActivationResult = Awaited<ReturnType<typeof activatePmAccount>>;

/** Shared outcome handling for both activation forms. */
function reportActivation(res: ActivationResult) {
  if (res.success) {
    Alert.alert(
      'Payment Merchant activated',
      'Your account is now live. You can start offering instant P2P buy & sell.',
      [{ text: 'Done', onPress: () => router.replace('/merchants/dashboard' as any) }]
    );
    return;
  }
  if (promptKycRefusal(res.code, res.message)) return;
  Alert.alert('Activation failed', res.message || 'Please try again.');
}

export default function ActivatePmScreen() {
  const { colors } = useTheme();
  const { activationNeedsId, loading } = useIsPaymentMerchant();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <Stack.Screen options={{ title: 'Activate Payment Merchant' }} />
      {loading ? <Spinner /> : activationNeedsId ? <IdActivationForm /> : <CentralActivation />}
    </SafeAreaView>
  );
}

/** Central inbound: nothing to mint, so activation is a single confirm. */
function CentralActivation() {
  const { spacing } = useTheme();
  const [submitting, setSubmitting] = useState(false);

  const handleSubmit = async () => {
    setSubmitting(true);
    const res = await activatePmAccount();
    setSubmitting(false);
    reportActivation(res);
  };

  return (
    <ScrollView contentContainerStyle={{ padding: spacing.lg }}>
      <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
        Your application was approved. Buyers pay into TSA Connect&apos;s account, and we send
        your naira to your settlement bank account as each trade settles.
      </Text>
      <Button
        title="Activate"
        variant="primary"
        onPress={handleSubmit}
        disabled={submitting}
        loading={submitting}
      />
    </ScrollView>
  );
}

/** Virtual-account mode: the BVN/NIN mints the account buyers pay into. */
function IdActivationForm() {
  const { colors, spacing, radius, fonts, type } = useTheme();
  const [idType, setIdType] = useState<PmIdType>('BVN');
  const [idNumber, setIdNumber] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [focused, setFocused] = useState(false);

  const valid = useMemo(() => isValidPmIdNumber(idNumber), [idNumber]);

  const handleSubmit = async () => {
    if (!valid) {
      Alert.alert('Check your entry', `${idType} must be exactly 11 digits.`);
      return;
    }
    setSubmitting(true);
    const res = await activatePmAccount(idType, idNumber);
    setSubmitting(false);
    reportActivation(res);
  };

  return (
    <KeyboardAvoidingView
      style={{ flex: 1 }}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
    >
      <ScrollView contentContainerStyle={{ padding: spacing.lg }} keyboardShouldPersistTaps="handled">
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Your application was approved. To go live, verify the account holder&apos;s identity —
          this creates the NGN account your buyers pay into.
        </Text>

        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
          ID type
        </Text>
        <View style={{ flexDirection: 'row', gap: spacing.sm, marginBottom: spacing.md }}>
          {ID_TYPES.map((t) => (
            <Chip
              key={t.value}
              label={t.label}
              selected={idType === t.value}
              onPress={() => setIdType(t.value)}
            />
          ))}
        </View>

        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
          {idType} (11 digits)
        </Text>
        <TextInput
          value={idNumber}
          onChangeText={(v) => setIdNumber(v.replace(/[^\d]/g, ''))}
          keyboardType="number-pad"
          maxLength={11}
          placeholder={idType === 'BVN' ? 'Bank Verification Number' : 'National Identification Number'}
          placeholderTextColor={colors.textMuted}
          onFocus={() => setFocused(true)}
          onBlur={() => setFocused(false)}
          style={{
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.md,
            borderRadius: radius.md,
            borderWidth: focused ? 2 : 1.5,
            borderColor: focused ? colors.accent : colors.border,
            backgroundColor: colors.surface,
            color: colors.textPrimary,
            fontFamily: fonts['400'],
            fontSize: type.body.fontSize,
            letterSpacing: 2,
          }}
        />

        {/* Privacy reassurance — the id number is not stored by TSA. */}
        <View
          style={{
            flexDirection: 'row',
            gap: spacing.sm,
            marginTop: spacing.lg,
            padding: spacing.md,
            borderRadius: radius.md,
            backgroundColor: colors.surface,
            borderWidth: 1,
            borderColor: colors.border,
          }}
        >
          <Icon name="shield" size={20} color={colors.info} />
          <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
            We don&apos;t store your {idType}. It&apos;s sent securely to our licensed payment
            provider to create your account and is not saved by TSA.
          </Text>
        </View>

        <Button
          title="Activate"
          variant="primary"
          onPress={handleSubmit}
          disabled={!valid || submitting}
          loading={submitting}
          style={{ marginTop: spacing.xl }}
        />
      </ScrollView>
    </KeyboardAvoidingView>
  );
}
