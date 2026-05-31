// app/pin-setup.tsx
// PIN creation/change screen.
//
// SECURITY screen: PIN entry/confirm, persistence (savePin / setBiometricEnabled),
// biometric verification, and all navigation are preserved verbatim — this is a
// visual/structural restyle onto the TSA Signature design system (theme tokens +
// @/components/ui primitives) only.
import React, { useState, useRef } from 'react';
import { View, TextInput, Pressable, Alert } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { setPin as savePin, setBiometricEnabled, isBiometricAvailable, getBiometricType } from '@/services/localAuth';
import { useTheme } from '@/theme';
import { Text, Button, Icon, PressableScale } from '@/components/ui';

type Step = 'create' | 'confirm' | 'biometric' | 'done';

const PinSetupScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const [step, setStep] = useState<Step>('create');
  const [pin, setPin] = useState('');
  const [confirmPin, setConfirmPin] = useState('');
  const [error, setError] = useState('');
  const inputRef = useRef<TextInput>(null);

  const handlePinInput = (text: string, isConfirm: boolean) => {
    const digits = text.replace(/[^0-9]/g, '').slice(0, 4);
    setError('');

    if (isConfirm) {
      setConfirmPin(digits);
      if (digits.length === 4) {
        if (digits === pin) {
          // PINs match — save and check biometric
          handlePinSet(digits);
        } else {
          setError('PINs do not match');
          setTimeout(() => setConfirmPin(''), 300);
        }
      }
    } else {
      setPin(digits);
      if (digits.length === 4) {
        setTimeout(() => {
          setStep('confirm');
          setConfirmPin('');
          setTimeout(() => inputRef.current?.focus(), 300);
        }, 200);
      }
    }
  };

  const handlePinSet = async (pinValue: string) => {
    await savePin(pinValue);

    // Check if biometric is available
    const bioAvailable = await isBiometricAvailable();
    if (bioAvailable) {
      setStep('biometric');
    } else {
      setStep('done');
      setTimeout(() => {
        if (router.canGoBack()) router.back();
        else router.replace('/home');
      }, 1500);
    }
  };

  const handleBiometricChoice = async (enable: boolean) => {
    if (enable) {
      // Verify biometric works before enabling
      const LocalAuth = await import('expo-local-authentication');
      const result = await LocalAuth.authenticateAsync({
        promptMessage: 'Verify your fingerprint or face',
        disableDeviceFallback: true,
      });
      if (!result.success) {
        Alert.alert('Verification Failed', 'Could not verify biometric. Please try again.');
        return;
      }
    }
    await setBiometricEnabled(enable);
    setStep('done');
    setTimeout(() => {
      if (router.canGoBack()) router.back();
      else router.replace('/home');
    }, 1500);
  };

  const iconCircle = (name: 'lock' | 'shield' | 'checkCircle', tint: string, bg: string) => (
    <View
      style={{
        width: 72,
        height: 72,
        borderRadius: radius.pill,
        backgroundColor: bg,
        justifyContent: 'center',
        alignItems: 'center',
        marginBottom: spacing.xl,
      }}
    >
      <Icon name={name} size={34} color={tint} />
    </View>
  );

  // Done screen
  if (step === 'done') {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.lg }}>
          {iconCircle('checkCircle', colors.success, colors.successBg)}
          <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            App Lock Enabled
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center">
            Your app is now secured
          </Text>
        </View>
      </SafeAreaView>
    );
  }

  // Biometric prompt
  if (step === 'biometric') {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.lg }}>
          <Pressable
            onPress={() => router.back()}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              alignSelf: 'flex-start',
              gap: spacing.xs,
              marginBottom: spacing['2xl'],
            }}
          >
            <Icon name="chevronLeft" size={22} color={colors.accent} />
          </Pressable>

          {iconCircle('shield', colors.accent, colors.accentMuted)}
          <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            Enable Biometric?
          </Text>
          <BiometricLabel />

          <Button title="Enable" onPress={() => handleBiometricChoice(true)} style={{ marginTop: spacing.sm }} />
          <Button
            title="Skip, use PIN only"
            variant="ghost"
            onPress={() => handleBiometricChoice(false)}
            style={{ marginTop: spacing.xs }}
          />
        </View>
      </SafeAreaView>
    );
  }

  // PIN input (create or confirm)
  const isConfirm = step === 'confirm';
  const currentPin = isConfirm ? confirmPin : pin;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.lg }}>
        <Pressable
          onPress={() => {
            if (isConfirm) {
              setStep('create');
              setPin('');
              setConfirmPin('');
            } else if (router.canGoBack()) router.back();
          }}
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            alignSelf: 'flex-start',
            gap: spacing.xs,
            marginBottom: spacing['2xl'],
          }}
        >
          <Icon name="chevronLeft" size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="600" color="accent">
            {isConfirm ? 'Re-enter' : 'Back'}
          </Text>
        </Pressable>

        {iconCircle('lock', colors.accent, colors.accentMuted)}
        <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
          {isConfirm ? 'Confirm PIN' : 'Create PIN'}
        </Text>
        <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing['3xl'] }}>
          {isConfirm ? 'Re-enter your 4-digit PIN' : 'Choose a 4-digit PIN to secure your app'}
        </Text>

        {/* PIN dots */}
        <View style={{ flexDirection: 'row', gap: spacing.xl, marginBottom: spacing['3xl'] }}>
          {[0, 1, 2, 3].map((i) => {
            const filled = currentPin.length > i;
            return (
              <View
                key={i}
                style={{
                  width: 18,
                  height: 18,
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
          ref={inputRef}
          style={{ position: 'absolute', opacity: 0, width: 1, height: 1 }}
          value={currentPin}
          onChangeText={(t) => handlePinInput(t, isConfirm)}
          keyboardType="number-pad"
          maxLength={4}
          secureTextEntry
          autoFocus
        />

        {/* Tap to focus */}
        <PressableScale onPress={() => inputRef.current?.focus()} style={{ paddingVertical: spacing.md }}>
          <Text variant="caption" color="textMuted">
            Tap here if keyboard dismissed
          </Text>
        </PressableScale>

        {error ? (
          <Text variant="bodySm" color="danger" align="center" style={{ marginTop: spacing.sm }}>
            {error}
          </Text>
        ) : null}
      </View>
    </SafeAreaView>
  );
};

// Shows which biometric type is available
const BiometricLabel = () => {
  const { spacing } = useTheme();
  const [label, setLabel] = useState('fingerprint or face');
  React.useEffect(() => {
    getBiometricType().then((t) => setLabel(t));
  }, []);
  return (
    <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing['2xl'] }}>
      Use {label} to unlock quickly
    </Text>
  );
};

export default PinSetupScreen;
