// app/reward-program/signup.tsx
//
// Signup reward form — one-time, paid as airtime or TradePoints. Eligibility is
// re-checked server-side on submit; this screen's gating is convenience only.
import React, { useEffect, useRef, useState } from 'react';
import { View, ScrollView, TextInput, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import { useAuth } from '@/AuthContext/AuthContext';
import { Button, Input, ScreenHeader, SelectRow, Sheet, Text } from '@/components/ui';
import { submitRewardClaim } from '@/services/rewardProgramApi';
import { PHONE_NETWORKS, validateSignupForm, type FormErrors } from '@/utils/rewardForms';

const NETWORK_LABEL: Record<string, string> = {
  mtn: 'MTN',
  airtel: 'Airtel',
  glo: 'Glo',
  '9mobile': '9mobile',
};

const SignupRewardForm = () => {
  const { colors, spacing } = useTheme();
  const kbHeight = useKeyboardHeight();
  // Identity is shown for confirmation but never submitted — the server takes
  // it from the auth token.
  const { currentUser } = useAuth();
  const [phoneNumber, setPhoneNumber] = useState('');
  const [phoneNetwork, setPhoneNetwork] = useState('');
  const [inviterUsername, setInviterUsername] = useState('');
  const [socialLinks, setSocialLinks] = useState('');

  const [errors, setErrors] = useState<FormErrors>({});
  const [validated, setValidated] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [networkOpen, setNetworkOpen] = useState(false);

  const phoneRef = useRef<TextInput>(null);
  const socialRef = useRef<TextInput>(null);

  const fields = { phoneNumber, phoneNetwork, inviterUsername, socialLinks };

  // Errors appear only after the first submit attempt, then track live.
  // Fields are rebuilt inside the effect so every input is a real dependency —
  // no eslint-disable, so adding a rule for socials/inviter can't silently stop
  // live validation for it.
  useEffect(() => {
    if (!validated) return;
    setErrors(validateSignupForm({ phoneNumber, phoneNetwork, inviterUsername, socialLinks }));
  }, [validated, phoneNumber, phoneNetwork, inviterUsername, socialLinks]);

  const handleSubmit = async () => {
    const found = validateSignupForm(fields);
    setErrors(found);
    setValidated(true);
    if (Object.keys(found).length > 0) return;

    setSubmitting(true);
    const r = await submitRewardClaim({
      kind: 'signup',
      phoneNumber: phoneNumber.trim(),
      phoneNetwork,
      inviterUsername: inviterUsername.trim(),
      socialLinks: socialLinks.trim(),
    });
    setSubmitting(false);

    if (r.success) {
      // Navigate first: on Android the alert is dismissible with the hardware
      // back button, which would skip an onPress-only navigation and leave the
      // user on a filled form that submits again.
      router.back();
      Alert.alert('Submitted', 'Your signup reward form has been received. We’ll review it and be in touch.');
      return;
    }
    Alert.alert('Could not submit', r.message || 'Please try again.');
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Signup form" fallbackRoute="/reward" />
      <KeyboardAvoidingView
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        style={{ flex: 1 }}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] + kbHeight }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
            A one-time reward for joining, paid as airtime or TradePoints.
          </Text>

          <Input
            label="Name"
            value={currentUser?.name ?? ''}
            editable={false}
            helper="From your account"
          />
          <Input
            label="Username"
            value={currentUser?.username ?? ''}
            editable={false}
            helper="From your account"
          />
          <Input
            ref={phoneRef}
            label="Phone line"
            value={phoneNumber}
            onChangeText={setPhoneNumber}
            placeholder="e.g. 08030000000"
            keyboardType="phone-pad"
            helper="Airtime is sent to this number"
            error={errors.phoneNumber}
          />
          <SelectRow
            label="Network"
            value={phoneNetwork ? NETWORK_LABEL[phoneNetwork] : ''}
            placeholder="Select network"
            onPress={() => setNetworkOpen(true)}
            error={errors.phoneNetwork}
          />
          <Input
            label="Inviter's username (optional)"
            value={inviterUsername}
            onChangeText={setInviterUsername}
            placeholder="Who invited you?"
            autoCapitalize="none"
            returnKeyType="next"
            onSubmitEditing={() => socialRef.current?.focus()}
          />
          <Input
            ref={socialRef}
            label="Social usernames (optional)"
            value={socialLinks}
            onChangeText={setSocialLinks}
            placeholder="e.g. @yourname on X, Instagram"
            autoCapitalize="none"
            helper="Your usernames on the socials you follow us on"
          />

          <Button
            title="Submit"
            onPress={handleSubmit}
            disabled={submitting}
            loading={submitting}
            style={{ marginTop: spacing.lg }}
          />
          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.md }}>
            Ensure to have done your KYC to be eligible.
          </Text>
        </ScrollView>
      </KeyboardAvoidingView>

      <Sheet visible={networkOpen} onClose={() => setNetworkOpen(false)} title="Select network">
        <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg }}>
          {PHONE_NETWORKS.map((n) => (
            <Button
              key={n}
              title={NETWORK_LABEL[n]}
              variant={phoneNetwork === n ? 'primary' : 'secondary'}
              onPress={() => {
                setPhoneNetwork(n);
                setNetworkOpen(false);
              }}
              style={{ marginBottom: spacing.xs }}
            />
          ))}
        </View>
      </Sheet>
    </SafeAreaView>
  );
};

export default SignupRewardForm;
