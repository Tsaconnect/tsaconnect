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
import { Button, Input, ScreenHeader, SelectRow, Sheet, Text } from '@/components/ui';
import { submitRewardClaim } from '@/services/rewardProgramApi';
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
  const [name, setName] = useState('');
  const [username, setUsername] = useState('');
  const [payoutMethod, setPayoutMethod] = useState('');
  const [payoutDetails, setPayoutDetails] = useState('');
  const [invitedUsernames, setInvitedUsernames] = useState('');

  const [errors, setErrors] = useState<FormErrors>({});
  const [validated, setValidated] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [methodOpen, setMethodOpen] = useState(false);

  const usernameRef = useRef<TextInput>(null);
  const detailsRef = useRef<TextInput>(null);

  const fields = { name, username, payoutMethod, payoutDetails, invitedUsernames };
  const listed = countUsernames(invitedUsernames);

  useEffect(() => {
    if (validated) setErrors(validateOnboardingForm(fields));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [validated, name, username, payoutMethod, payoutDetails, invitedUsernames]);

  const handleSubmit = async () => {
    const found = validateOnboardingForm(fields);
    setErrors(found);
    setValidated(true);
    if (Object.keys(found).length > 0) return;

    setSubmitting(true);
    const r = await submitRewardClaim({
      kind: 'onboarding',
      name: name.trim(),
      username: username.trim(),
      payoutMethod,
      payoutDetails: payoutDetails.trim(),
      invitedUsernames: invitedUsernames.trim(),
    });
    setSubmitting(false);

    if (r.success) {
      Alert.alert(
        'Submitted',
        'Your onboarding reward form has been received. We’ll review it and be in touch.',
        [{ text: 'Done', onPress: () => router.back() }],
      );
      return;
    }
    Alert.alert('Could not submit', r.message || 'Please try again.');
  };

  const methodLabel = PAYOUT_METHODS.find((m) => m.key === payoutMethod)?.label ?? '';

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Onboarding form" fallbackRoute="/reward" />
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
            A one-time cash reward for bringing people onto TSA Connect.
          </Text>

          <Input
            label="Name"
            value={name}
            onChangeText={setName}
            placeholder="Your full name"
            error={errors.name}
            returnKeyType="next"
            onSubmitEditing={() => usernameRef.current?.focus()}
          />
          <Input
            ref={usernameRef}
            label="Username"
            value={username}
            onChangeText={setUsername}
            placeholder="Your TSA Connect username"
            autoCapitalize="none"
            error={errors.username}
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
            helper={`${listed} of ${MIN_ONBOARDING_USERNAMES} listed`}
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
            Ensure you onboard a minimum of {MIN_ONBOARDING_USERNAMES} users and also make a
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
