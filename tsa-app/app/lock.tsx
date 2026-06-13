// app/lock.tsx
// Lock screen — biometric prompt + PIN fallback.
// Migrated onto the TSA Signature design system (@/theme tokens + @/components/ui
// primitives). SECURITY: the biometric auth, PIN entry/verification, attempt
// handling, and unlock navigation are preserved verbatim — visual/structural only.
import React, { useState, useEffect, useRef } from 'react';
import { View, TextInput, Pressable } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import {
  authenticateWithBiometric, verifyPin, getLockState, getBiometricType,
} from '@/services/localAuth';
import { useTheme } from '@/theme';
import { Icon, PressableScale, Spinner, Text } from '@/components/ui';

const LockScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const [pin, setPin] = useState('');
  const [error, setError] = useState('');
  const [showPinInput, setShowPinInput] = useState(false);
  const [loading, setLoading] = useState(true);
  const [biometricType, setBiometricType] = useState('Biometric');
  const pinRef = useRef<TextInput>(null);

  useEffect(() => {
    tryBiometric();
  }, []);

  const tryBiometric = async () => {
    const state = await getLockState();
    const type = await getBiometricType();
    setBiometricType(type);

    if (state.hasBiometric) {
      setLoading(false);
      const success = await authenticateWithBiometric();
      if (success) {
        router.replace('/home');
        return;
      }
      // Biometric failed — show PIN
      if (state.hasPin) {
        setShowPinInput(true);
        setTimeout(() => pinRef.current?.focus(), 300);
      }
    } else if (state.hasPin) {
      setLoading(false);
      setShowPinInput(true);
      setTimeout(() => pinRef.current?.focus(), 300);
    } else {
      // No lock configured — skip
      router.replace('/home');
    }
  };

  const handlePinSubmit = async () => {
    if (pin.length !== 4) {
      setError('Enter your 4-digit PIN');
      return;
    }
    const valid = await verifyPin(pin);
    if (valid) {
      router.replace('/home');
    } else {
      setError('Incorrect PIN');
      setPin('');
    }
  };

  const handlePinChange = (text: string) => {
    const digits = text.replace(/[^0-9]/g, '').slice(0, 4);
    setPin(digits);
    setError('');
    if (digits.length === 4) {
      setTimeout(async () => {
        const valid = await verifyPin(digits);
        if (valid) {
          router.replace('/home');
        } else {
          setError('Incorrect PIN');
          setPin('');
        }
      }, 100);
    }
  };

  if (loading) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner size="large" />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View
        style={{
          flex: 1,
          justifyContent: 'center',
          alignItems: 'center',
          paddingHorizontal: spacing['3xl'],
        }}
      >
        {/* Logo / Icon */}
        <View
          style={{
            width: 72,
            height: 72,
            borderRadius: radius.pill,
            backgroundColor: colors.accentMuted,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.xl,
          }}
        >
          <Icon name="lock" size={32} color={colors.accent} />
        </View>
        <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
          TSA Connect
        </Text>
        <Text
          variant="bodySm"
          color="textSecondary"
          align="center"
          style={{ marginBottom: spacing['3xl'] }}
        >
          {showPinInput ? 'Enter your PIN to unlock' : `Use ${biometricType} to unlock`}
        </Text>

        {/* PIN input */}
        {showPinInput && (
          <View style={{ alignItems: 'center', width: '100%' }}>
            {/* PIN dots */}
            <View style={{ flexDirection: 'row', gap: spacing.lg, marginBottom: spacing['2xl'] }}>
              {[0, 1, 2, 3].map(i => {
                const filled = pin.length > i;
                return (
                  <View
                    key={i}
                    style={{
                      width: 16,
                      height: 16,
                      borderRadius: radius.pill,
                      borderWidth: 2,
                      borderColor: filled ? colors.accent : colors.border,
                      backgroundColor: filled ? colors.accent : 'transparent',
                    }}
                  />
                );
              })}
            </View>

            <TextInput
              ref={pinRef}
              style={{ position: 'absolute', opacity: 0, width: 1, height: 1 }}
              value={pin}
              onChangeText={handlePinChange}
              keyboardType="number-pad"
              maxLength={4}
              secureTextEntry
              autoFocus
            />

            {error ? (
              <Text variant="bodySm" color="danger" align="center" style={{ marginTop: spacing.sm }}>
                {error}
              </Text>
            ) : null}
          </View>
        )}

        {/* Biometric retry button */}
        {!showPinInput && (
          <PressableScale
            onPress={tryBiometric}
            style={{ alignItems: 'center', paddingVertical: spacing['2xl'] }}
          >
            {/* Registry lacks dedicated `fingerprint`/`scan` glyphs (reported in
                iconsNeeded); `lock` is the closest registered unlock affordance. */}
            <Icon name="lock" size={48} color={colors.accent} />
            <Text variant="bodySm" color="textSecondary" align="center" style={{ marginTop: spacing.md }}>
              Tap to unlock with {biometricType}
            </Text>
          </PressableScale>
        )}

        {/* Switch between biometric and PIN */}
        <Pressable
          style={{ marginTop: spacing['2xl'], paddingVertical: spacing.md }}
          onPress={() => {
            if (showPinInput) {
              tryBiometric();
              setShowPinInput(false);
            } else {
              setShowPinInput(true);
              setTimeout(() => pinRef.current?.focus(), 300);
            }
          }}
        >
          <Text variant="bodySm" weight="600" color="accent" align="center">
            {showPinInput ? `Use ${biometricType} instead` : 'Use PIN instead'}
          </Text>
        </Pressable>
      </View>
    </SafeAreaView>
  );
};

export default LockScreen;
