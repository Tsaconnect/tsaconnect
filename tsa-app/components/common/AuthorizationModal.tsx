// components/common/AuthorizationModal.tsx
//
// Authorization (PIN/biometric) modal — gates sensitive actions (e.g. sending
// funds). Restyled onto the TSA Signature design system (@/theme +
// @/components/ui): the legacy RN Modal + hardcoded COLORS chrome is replaced by
// the Sheet primitive, tokenized typography/spacing, and primitive controls.
// SECURITY: all auth logic is preserved verbatim — the biometric/PIN gating,
// PIN attempt counting (3-strikes lockout), verifyPin/authenticateWithBiometric
// calls, and the onAuthorized/onCancel callbacks (and props/exports) are
// unchanged. Visual/structural restyle only. With neither a PIN nor biometrics
// (a customer who has just signed up to pay a link), it offers to set up a PIN
// — pin-setup goes back to where she was once it is done.
import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import {
  authenticateWithBiometric,
  getBiometricType,
  hasPin,
  isBiometricAvailable,
  verifyPin,
} from '../../services/localAuth';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Card,
  EmptyState,
  Icon,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  Text,
} from '@/components/ui';

interface AuthorizationModalProps {
  visible: boolean;
  title?: string;
  description?: string;
  onAuthorized: () => void;
  onCancel: () => void;
}

type AuthMethod = 'choose' | 'pin';

export default function AuthorizationModal({
  visible,
  title = 'Authorize',
  description,
  onAuthorized,
  onCancel,
}: AuthorizationModalProps) {
  const { colors, spacing } = useTheme();

  const [method, setMethod] = useState<AuthMethod>('choose');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const [bioAvailable, setBioAvailable] = useState(false);
  const [bioType, setBioType] = useState('Fingerprint');

  const [pinAvailable, setPinAvailable] = useState(false);
  const [pinValue, setPinValue] = useState('');
  const [pinAttempts, setPinAttempts] = useState(0);

  useEffect(() => {
    if (!visible) {
      setMethod('choose');
      setError('');
      setPinValue('');
      setPinAttempts(0);
      setLoading(false);
      return;
    }

    (async () => {
      const [bioOk, pinOk, type] = await Promise.all([
        isBiometricAvailable(),
        hasPin(),
        getBiometricType(),
      ]);

      setBioAvailable(bioOk);
      setPinAvailable(pinOk);
      setBioType(type);
    })();
  }, [visible]);

  const handleBiometric = async () => {
    setError('');
    const success = await authenticateWithBiometric();
    if (success) {
      onAuthorized();
      return;
    }
    setError('Biometric verification failed. Try another method.');
  };

  const handlePinSubmit = async (pin: string) => {
    setError('');
    setLoading(true);

    const valid = await verifyPin(pin);

    setLoading(false);

    if (valid) {
      onAuthorized();
      return;
    }

    const attempts = pinAttempts + 1;
    setPinAttempts(attempts);
    setPinValue('');

    if (attempts >= 3) {
      setPinAttempts(0);
      setError(
        bioAvailable
          ? 'Too many incorrect PIN attempts. Use biometrics or cancel.'
          : 'Too many incorrect PIN attempts. Close this prompt and try again.'
      );
      if (bioAvailable) {
        setMethod('choose');
      }
      return;
    }

    setError(`Incorrect PIN. ${3 - attempts} attempts remaining.`);
  };

  const renderMethodTile = (
    icon: 'fingerprint' | 'scan' | 'keypad',
    label: string,
    onPress: () => void
  ) => (
    <PressableScale onPress={onPress}>
      <Card padding="md">
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <View
            style={{
              width: 44,
              height: 44,
              borderRadius: 22,
              alignItems: 'center',
              justifyContent: 'center',
              backgroundColor: colors.accentMuted,
            }}
          >
            <Icon name={icon} size={24} color={colors.accent} />
          </View>
          <Text variant="bodyMed" weight="600" style={{ flex: 1 }}>
            {label}
          </Text>
          <Icon name="chevronRight" size={18} color={colors.textMuted} />
        </View>
      </Card>
    </PressableScale>
  );

  const renderChoose = () => (
    <View style={{ gap: spacing.md, marginTop: spacing.sm }}>
      {bioAvailable &&
        renderMethodTile(
          bioType === 'Face ID' ? 'scan' : 'fingerprint',
          `Use ${bioType}`,
          handleBiometric
        )}

      {pinAvailable && renderMethodTile('keypad', 'Use PIN', () => setMethod('pin'))}

      {!bioAvailable && !pinAvailable && (
        <EmptyState
          icon="shield"
          title="Set up a PIN first"
          subtitle="You approve payments with an app PIN or biometrics. Set a PIN now, then come back and approve."
          actionLabel="Set up PIN"
          onAction={() => { onCancel(); router.push('/pin-setup'); }}
        />
      )}
    </View>
  );

  const renderPin = () => (
    <View style={{ alignItems: 'center', marginTop: spacing.sm, gap: spacing.lg }}>
      <Text variant="bodySm" color="textSecondary" align="center">
        Enter your 4-digit PIN
      </Text>
      <Input
        containerStyle={{ width: 220 }}
        inputStyle={{
          textAlign: 'center',
          fontSize: 28,
          letterSpacing: 12,
        }}
        value={pinValue}
        onChangeText={(text) => {
          setPinValue(text);
          if (text.length === 4) {
            handlePinSubmit(text);
          }
        }}
        keyboardType="number-pad"
        maxLength={4}
        secureTextEntry
        autoFocus
        placeholder="••••"
      />
      {loading && <Spinner inline />}
      <Button
        title="Try another method"
        variant="ghost"
        size="sm"
        fullWidth={false}
        onPress={() => setMethod('choose')}
      />
    </View>
  );

  return (
    <Sheet visible={visible} onClose={onCancel} title={title}>
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.md }}>
        {description && (
          <Text variant="bodySm" color="textSecondary">
            {description}
          </Text>
        )}

        {error !== '' && (
          <AccentCard
            railColor={colors.danger}
            padding="md"
            style={{ backgroundColor: colors.dangerBg }}
          >
            <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
              <Icon name="dispute" size={16} color={colors.danger} />
              <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                {error}
              </Text>
            </View>
          </AccentCard>
        )}

        {method === 'choose' && renderChoose()}
        {method === 'pin' && renderPin()}
      </View>
    </Sheet>
  );
}
