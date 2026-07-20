// app/reward-program/signup.tsx
//
// Signup reward form — one-time, paid as airtime or TradePoints. Eligibility is
// re-checked server-side on submit; this screen's gating is convenience only.
import React, { useEffect, useRef, useState } from 'react';
import { View, ScrollView, TextInput, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
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
  const [name, setName] = useState('');
  const [username, setUsername] = useState('');
  const [phoneNumber, setPhoneNumber] = useState('');
  const [phoneNetwork, setPhoneNetwork] = useState('');
  const [inviterUsername, setInviterUsername] = useState('');
  const [socialLinks, setSocialLinks] = useState('');

  const [errors, setErrors] = useState<FormErrors>({});
  const [validated, setValidated] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [networkOpen, setNetworkOpen] = useState(false);

  const usernameRef = useRef<TextInput>(null);
  const phoneRef = useRef<TextInput>(null);
  const inviterRef = useRef<TextInput>(null);
  const socialRef = useRef<TextInput>(null);

  const fields = { name, username, phoneNumber, phoneNetwork, inviterUsername, socialLinks };

  // Errors appear only after the first submit attempt, then track live.
  useEffect(() => {
    if (validated) setErrors(validateSignupForm(fields));
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [validated, name, username, phoneNumber, phoneNetwork]);

  const handleSubmit = async () => {
    const found = validateSignupForm(fields);
    setErrors(found);
    setValidated(true);
    if (Object.keys(found).length > 0) return;

    setSubmitting(true);
    const r = await submitRewardClaim({
      kind: 'signup',
      name: name.trim(),
      username: username.trim(),
      phoneNumber: phoneNumber.trim(),
      phoneNetwork,
      inviterUsername: inviterUsername.trim(),
      socialLinks: socialLinks.trim(),
    });
    setSubmitting(false);

    if (r.success) {
      Alert.alert(
        'Submitted',
        'Your signup reward form has been received. We’ll review it and be in touch.',
        [{ text: 'Done', onPress: () => router.back() }],
      );
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
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
            A one-time reward for joining, paid as airtime or TradePoints.
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
            returnKeyType="next"
            onSubmitEditing={() => phoneRef.current?.focus()}
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
            ref={inviterRef}
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
            label="Social links (optional)"
            value={socialLinks}
            onChangeText={setSocialLinks}
            placeholder="Links to the socials you followed"
            autoCapitalize="none"
            helper="Paste the profiles you follow us on"
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
