// app/reward-program/onboarding.tsx
//
// Onboarding reward form — one-time cash reward, unlocked only after the user
// has onboarded MIN_ONBOARDING_USERNAMES KYC-verified users and made at least
// one transaction. The server re-checks both gates on submit.
import React, { useEffect, useRef, useState } from 'react';
import { View, ScrollView, TextInput, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import { useAuth } from '@/AuthContext/AuthContext';
import { Button, Input, ScreenHeader, SelectRow, Sheet, Text } from '@/components/ui';
import { getMyRewardClaims, submitRewardClaim } from '@/services/rewardProgramApi';
import {
  countUsernames,
  MIN_ONBOARDING_USERNAMES,
  validateOnboardingForm,
  type FormErrors,
} from '@/utils/rewardForms';

const PAYOUT_METHODS: { key: string; label: string }[] = [
  { key: 'bank', label: 'Bank account' },
  { key: 'mobile_money', label: 'Mobile money' },
  { key: 'token', label: 'Crypto (token payout)' },
];

const OnboardingRewardForm = () => {
  const { colors, spacing } = useTheme();
  const kbHeight = useKeyboardHeight();
  const { currentUser } = useAuth();
  // The threshold is the SERVER's — a hardcoded client copy would silently
  // disagree with the hub (which already reads requiredReferrals) if it changed.
  const [minUsernames, setMinUsernames] = useState(MIN_ONBOARDING_USERNAMES);
  const [payoutMethod, setPayoutMethod] = useState('');
  const [payoutDetails, setPayoutDetails] = useState('');
  const [invitedUsernames, setInvitedUsernames] = useState('');

  const [errors, setErrors] = useState<FormErrors>({});
  const [validated, setValidated] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [methodOpen, setMethodOpen] = useState(false);

  const detailsRef = useRef<TextInput>(null);

  const fields = { payoutMethod, payoutDetails, invitedUsernames };
  const listed = countUsernames(invitedUsernames);

  useEffect(() => {
    let cancelled = false;
    getMyRewardClaims().then((r) => {
      const required = r.data?.onboarding?.eligibility?.requiredReferrals;
      if (!cancelled && typeof required === 'number' && required > 0) setMinUsernames(required);
    });
    return () => { cancelled = true; };
  }, []);

  useEffect(() => {
    if (!validated) return;
    setErrors(
      validateOnboardingForm({ payoutMethod, payoutDetails, invitedUsernames }, minUsernames),
    );
  }, [validated, payoutMethod, payoutDetails, invitedUsernames, minUsernames]);

  const handleSubmit = async () => {
    const found = validateOnboardingForm(fields, minUsernames);
    setErrors(found);
    setValidated(true);
    if (Object.keys(found).length > 0) return;

    setSubmitting(true);
    const r = await submitRewardClaim({
      kind: 'onboarding',
      payoutMethod,
      payoutDetails: payoutDetails.trim(),
      invitedUsernames: invitedUsernames.trim(),
    });
    setSubmitting(false);

    if (r.success) {
      router.back();
      Alert.alert('Submitted', 'Your onboarding reward form has been received. We’ll review it and be in touch.');
      return;
    }
    Alert.alert('Could not submit', r.message || 'Please try again.');
  };

  const methodLabel = PAYOUT_METHODS.find((m) => m.key === payoutMethod)?.label ?? '';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Onboarding form" />
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
            A one-time cash reward for bringing people onto TSA Connect.
          </Text>

          <Input label="Name" value={currentUser?.name ?? ''} editable={false} helper="From your account" />
          <Input
            label="Username"
            value={currentUser?.username ?? ''}
            editable={false}
            helper="From your account"
          />
          <SelectRow
            label="Payout method"
            value={methodLabel}
            placeholder="How should we pay you?"
            onPress={() => setMethodOpen(true)}
            error={errors.payoutMethod}
          />
          <Input
            ref={detailsRef}
            label="Account details"
            value={payoutDetails}
            onChangeText={setPayoutDetails}
            placeholder="Account number and bank, or mobile money number"
            error={errors.payoutDetails}
          />
          <Input
            label="Invited usernames"
            value={invitedUsernames}
            onChangeText={setInvitedUsernames}
            placeholder="Separate usernames with commas"
            autoCapitalize="none"
            multiline
            helper={`${listed} of ${minUsernames} listed`}
            error={errors.invitedUsernames}
          />

          <Button
            title="Submit"
            onPress={handleSubmit}
            disabled={submitting}
            loading={submitting}
            style={{ marginTop: spacing.lg }}
          />
          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.md }}>
            Ensure you onboard a minimum of {minUsernames} users and also make a
            transaction to be eligible.
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>

      <Sheet visible={methodOpen} onClose={() => setMethodOpen(false)} title="Payout method">
        <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg }}>
          {PAYOUT_METHODS.map((m) => (
            <Button
              key={m.key}
              title={m.label}
              variant={payoutMethod === m.key ? 'primary' : 'secondary'}
              onPress={() => {
                setPayoutMethod(m.key);
                setMethodOpen(false);
              }}
              style={{ marginBottom: spacing.xs }}
            />
          ))}
        </View>
      </Sheet>
    </SafeAreaView>
  );
};

export default OnboardingRewardForm;
