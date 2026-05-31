// app/merchants/merchant-request.tsx
//
// Become-a-merchant request form. Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives). This is a visual/structural
// restyle ONLY — the existing-request fetch, AsyncStorage role write on approval,
// the required-field validation, the submitMerchantRequest payload (optional
// fields collapsed to `undefined`), the pending/approved/rejected status gating,
// the focus-chain refs, and every navigation route are all preserved verbatim.
//
// NOTE: the fields use a local token-themed TextInput `Field` (not the @/components/ui
// `Input`) because the focus-chain — businessName → … → submit, driven by the
// onSubmitEditing refs — must keep working, and the Input primitive does not yet
// forward a ref to its inner TextInput. `Field` mirrors Input's chrome exactly.
import React, { useState, useEffect, useRef } from 'react';
import {
  View,
  TextInput,
  TextInputProps,
  ScrollView,
  Alert,
  KeyboardAvoidingView,
  Platform,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router } from 'expo-router';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useAuth } from '../../AuthContext/AuthContext';
import { getMyMerchantRequest, submitMerchantRequest } from '../../components/services/api';
import { useTheme } from '@/theme';
import { Button, Chip, Icon, Spinner, Text } from '@/components/ui';

const BUSINESS_TYPES = [
  { label: 'General Products', value: 'general_products' },
  { label: 'Digital Products', value: 'digital_products' },
  { label: 'P2P Merchant', value: 'p2p_merchant' },
  { label: 'Service Provider', value: 'service_provider' },
];

export default function MerchantRequestScreen() {
  const { colors, spacing } = useTheme();
  const { currentUser } = useAuth();
  const [loading, setLoading] = useState(true);
  const [submitting, setSubmitting] = useState(false);
  const [existingRequest, setExistingRequest] = useState<any>(null);

  const [businessType, setBusinessType] = useState('general_products');
  const [businessName, setBusinessName] = useState('');
  const [businessDescription, setBusinessDescription] = useState('');
  const [address, setAddress] = useState('');
  const [city, setCity] = useState('');
  const [state, setState] = useState('');
  const [country, setCountry] = useState('');
  const [phone, setPhone] = useState(currentUser?.phoneNumber || '');
  const [registrationNumber, setRegistrationNumber] = useState('');

  const scrollRef = useRef<ScrollView>(null);
  const businessNameRef = useRef<TextInput>(null);
  const descriptionRef = useRef<TextInput>(null);
  const addressRef = useRef<TextInput>(null);
  const cityRef = useRef<TextInput>(null);
  const stateRef = useRef<TextInput>(null);
  const countryRef = useRef<TextInput>(null);
  const phoneRef = useRef<TextInput>(null);
  const regNumberRef = useRef<TextInput>(null);

  useEffect(() => {
    fetchExistingRequest();
  }, []);

  const fetchExistingRequest = async () => {
    setLoading(true);
    try {
      const res = await getMyMerchantRequest();
      if (res.success && res.data) {
        setExistingRequest(res.data);
        if (res.data.status === 'approved') {
          await AsyncStorage.setItem('role', 'merchant');
        }
      } else if (!res.success) {
        Alert.alert('Error', res.message || 'Could not check your application status. Please try again.');
      }
    } catch (error) {
      Alert.alert('Error', 'Network error. Please check your connection and try again.');
    } finally {
      setLoading(false);
    }
  };

  const handleSubmit = async () => {
    if (!businessName || !address || !city || !state || !country || !phone) {
      Alert.alert('Error', 'Please fill in all required fields');
      return;
    }

    setSubmitting(true);
    const res = await submitMerchantRequest({
      businessType,
      businessName,
      businessDescription: businessDescription || undefined,
      address,
      city,
      state,
      country,
      phone,
      registrationNumber: registrationNumber || undefined,
    });

    setSubmitting(false);
    if (res.success) {
      Alert.alert('Success', 'Your merchant application has been submitted!');
      setExistingRequest(res.data);
    } else {
      Alert.alert('Error', res.message || 'Failed to submit application');
    }
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <Stack.Screen options={{ title: 'Merchant Application' }} />
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" label="Loading…" />
        </View>
      </SafeAreaView>
    );
  }

  if (existingRequest) {
    const { status } = existingRequest;

    if (status === 'pending') {
      return (
        <StatusScreen
          icon="clock"
          tone={colors.warning}
          toneBg={colors.warningBg}
          title="Application Under Review"
          body={`Your merchant application for "${existingRequest.businessName}" is being reviewed. We'll update your status soon.`}
          actionTitle="Go Back"
          actionVariant="secondary"
          onAction={() => router.back()}
        />
      );
    }

    if (status === 'approved') {
      return (
        <StatusScreen
          icon="checkCircle"
          tone={colors.success}
          toneBg={colors.successBg}
          title="You're a Merchant!"
          body="Your application has been approved. You now have access to the merchant portal."
          actionTitle="Go to Merchant Dashboard"
          actionVariant="success"
          onAction={() => router.push('/merchants/dashboard')}
        />
      );
    }

    if (status === 'rejected') {
      return (
        <StatusScreen
          icon="closeCircle"
          tone={colors.danger}
          toneBg={colors.dangerBg}
          title="Application Rejected"
          body={`Reason: ${existingRequest.adminNote || 'No reason provided'}`}
          actionTitle="Apply Again"
          actionVariant="primary"
          onAction={() => {
            setExistingRequest(null);
            setBusinessName('');
            setBusinessDescription('');
            setAddress('');
            setCity('');
            setState('');
            setCountry('');
            setRegistrationNumber('');
          }}
        />
      );
    }
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen options={{ title: 'Merchant Application' }} />
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
        keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
      >
        <ScrollView
          ref={scrollRef}
          keyboardShouldPersistTaps="handled"
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        >
          <Text variant="title1" weight="700">
            Become a Merchant
          </Text>
          <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.xl }}>
            Fill in your business details to apply
          </Text>

          <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.sm }}>
            Business Type *
          </Text>
          <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
            {BUSINESS_TYPES.map((bt) => (
              <Chip
                key={bt.value}
                label={bt.label}
                selected={businessType === bt.value}
                onPress={() => setBusinessType(bt.value)}
              />
            ))}
          </View>

          <Field
            ref={businessNameRef}
            label="Business Name *"
            value={businessName}
            onChangeText={setBusinessName}
            placeholder="e.g. Business Name"
            returnKeyType="next"
            onSubmitEditing={() => descriptionRef.current?.focus()}
          />

          <Field
            ref={descriptionRef}
            label="Business Description"
            value={businessDescription}
            onChangeText={setBusinessDescription}
            placeholder="Describe your business..."
            multiline
            multilineHeight={80}
            returnKeyType="next"
            blurOnSubmit={true}
            onSubmitEditing={() => addressRef.current?.focus()}
          />

          <Field
            ref={addressRef}
            label="Address *"
            value={address}
            onChangeText={setAddress}
            placeholder="Street address"
            returnKeyType="next"
            onSubmitEditing={() => cityRef.current?.focus()}
          />

          <Field
            ref={cityRef}
            label="City *"
            value={city}
            onChangeText={setCity}
            placeholder="City"
            returnKeyType="next"
            onSubmitEditing={() => stateRef.current?.focus()}
          />

          <Field
            ref={stateRef}
            label="State *"
            value={state}
            onChangeText={setState}
            placeholder="State"
            returnKeyType="next"
            onSubmitEditing={() => countryRef.current?.focus()}
          />

          <Field
            ref={countryRef}
            label="Country *"
            value={country}
            onChangeText={setCountry}
            placeholder="Country"
            returnKeyType="next"
            onSubmitEditing={() => phoneRef.current?.focus()}
          />

          <Field
            ref={phoneRef}
            label="Phone *"
            value={phone}
            onChangeText={setPhone}
            placeholder="+234..."
            keyboardType="phone-pad"
            returnKeyType="next"
            onSubmitEditing={() => regNumberRef.current?.focus()}
          />

          <Field
            ref={regNumberRef}
            label="Registration Number (optional)"
            value={registrationNumber}
            onChangeText={setRegistrationNumber}
            placeholder="e.g. RC-123456"
            returnKeyType="done"
            onSubmitEditing={handleSubmit}
          />

          <Button
            title="Submit Application"
            onPress={handleSubmit}
            disabled={submitting}
            loading={submitting}
            style={{ marginTop: spacing.xl }}
          />
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

// Token-themed labelled text field. Mirrors the @/components/ui Input chrome
// (label, accent focus border, surface fill, token radius/spacing) while
// forwarding its ref to the underlying TextInput so the onSubmitEditing focus
// chain keeps working exactly as before.
interface FieldProps extends Omit<TextInputProps, 'style'> {
  label: string;
  multilineHeight?: number;
}

const Field = React.forwardRef<TextInput, FieldProps>(
  ({ label, multilineHeight, onFocus, onBlur, ...rest }, ref) => {
    const { colors, radius, spacing, fonts, type } = useTheme();
    const [focused, setFocused] = useState(false);
    return (
      <View style={{ marginTop: spacing.md }}>
        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
          {label}
        </Text>
        <TextInput
          ref={ref}
          placeholderTextColor={colors.textMuted}
          onFocus={(e) => {
            setFocused(true);
            onFocus?.(e);
          }}
          onBlur={(e) => {
            setFocused(false);
            onBlur?.(e);
          }}
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
            ...(multilineHeight ? { height: multilineHeight, textAlignVertical: 'top' } : null),
          }}
          {...rest}
        />
      </View>
    );
  }
);
Field.displayName = 'Field';

// Shared status panel for the pending / approved / rejected branches — keeps the
// status registration + actions identical, only the chrome is themed.
const StatusScreen: React.FC<{
  icon: 'clock' | 'checkCircle' | 'closeCircle';
  tone: string;
  toneBg: string;
  title: string;
  body: string;
  actionTitle: string;
  actionVariant: 'primary' | 'secondary' | 'success';
  onAction: () => void;
}> = ({ icon, tone, toneBg, title, body, actionTitle, actionVariant, onAction }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen options={{ title: 'Merchant Application' }} />
      <View style={{ flex: 1, justifyContent: 'center', paddingHorizontal: spacing.lg }}>
        <View
          style={{
            backgroundColor: colors.surface,
            borderRadius: radius.lg,
            borderWidth: 1,
            borderColor: tone,
            padding: spacing['2xl'],
            alignItems: 'center',
          }}
        >
          <View
            style={{
              width: 72,
              height: 72,
              borderRadius: radius.pill,
              backgroundColor: toneBg,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
            }}
          >
            <Icon name={icon} size={36} color={tone} />
          </View>
          <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            {title}
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing.xl }}>
            {body}
          </Text>
          <Button title={actionTitle} variant={actionVariant} onPress={onAction} />
        </View>
      </View>
    </SafeAreaView>
  );
};
