// app/pin-setup.tsx
// PIN creation/change screen
import React, { useState, useRef } from 'react';
import {
  View, Text, StyleSheet, TextInput, TouchableOpacity, Pressable, Alert,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import { setPin as savePin, setBiometricEnabled, isBiometricAvailable, getBiometricType } from '@/services/localAuth';

const GOLD = '#D4AF37';
type Step = 'create' | 'confirm' | 'biometric' | 'done';

const PinSetupScreen = () => {
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

  // Done screen
  if (step === 'done') {
    return (
      <View style={s.container}>
        <View style={s.content}>
          <View style={[s.iconCircle, { backgroundColor: '#D1FAE5' }]}>
            <Ionicons name="checkmark-circle" size={36} color="#16A34A" />
          </View>
          <Text style={s.title}>App Lock Enabled</Text>
          <Text style={s.subtitle}>Your app is now secured</Text>
        </View>
      </View>
    );
  }

  // Biometric prompt
  if (step === 'biometric') {
    return (
      <View style={s.container}>
        <View style={s.content}>
          <Pressable onPress={() => router.back()} style={s.backRow}>
            <Ionicons name="chevron-back" size={22} color={GOLD} />
          </Pressable>
          <View style={s.iconCircle}>
            <Ionicons name="finger-print" size={36} color={GOLD} />
          </View>
          <Text style={s.title}>Enable Biometric?</Text>
          <BiometricLabel />
          <TouchableOpacity style={s.btn} onPress={() => handleBiometricChoice(true)}>
            <Text style={s.btnT}>Enable</Text>
          </TouchableOpacity>
          <TouchableOpacity style={s.btn2} onPress={() => handleBiometricChoice(false)}>
            <Text style={s.btn2T}>Skip, use PIN only</Text>
          </TouchableOpacity>
        </View>
      </View>
    );
  }

  // PIN input (create or confirm)
  const isConfirm = step === 'confirm';
  const currentPin = isConfirm ? confirmPin : pin;

  return (
    <View style={s.container}>
      <View style={s.content}>
        <Pressable onPress={() => {
          if (isConfirm) { setStep('create'); setPin(''); setConfirmPin(''); }
          else if (router.canGoBack()) router.back();
        }} style={s.backRow}>
          <Ionicons name="chevron-back" size={22} color={GOLD} />
          <Text style={s.backT}>{isConfirm ? 'Re-enter' : 'Back'}</Text>
        </Pressable>

        <View style={s.iconCircle}>
          <Ionicons name="keypad" size={32} color={GOLD} />
        </View>
        <Text style={s.title}>{isConfirm ? 'Confirm PIN' : 'Create PIN'}</Text>
        <Text style={s.subtitle}>
          {isConfirm ? 'Re-enter your 4-digit PIN' : 'Choose a 4-digit PIN to secure your app'}
        </Text>

        {/* PIN dots */}
        <View style={s.pinDots}>
          {[0, 1, 2, 3].map(i => (
            <View key={i} style={[s.dot, currentPin.length > i && s.dotFilled]} />
          ))}
        </View>

        <TextInput
          ref={inputRef}
          style={s.hiddenInput}
          value={currentPin}
          onChangeText={(t) => handlePinInput(t, isConfirm)}
          keyboardType="number-pad"
          maxLength={4}
          secureTextEntry
          autoFocus
        />

        {/* Tap to focus */}
        <TouchableOpacity onPress={() => inputRef.current?.focus()} style={s.tapArea}>
          <Text style={s.tapText}>Tap here if keyboard dismissed</Text>
        </TouchableOpacity>

        {error ? <Text style={s.error}>{error}</Text> : null}
      </View>
    </View>
  );
};

// Shows which biometric type is available
const BiometricLabel = () => {
  const [label, setLabel] = useState('fingerprint or face');
  React.useEffect(() => {
    getBiometricType().then(t => setLabel(t));
  }, []);
  return <Text style={{ fontSize: 15, color: '#888', marginBottom: 24 }}>Use {label} to unlock quickly</Text>;
};

export default PinSetupScreen;

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA', justifyContent: 'center' },
  content: { alignItems: 'center', padding: 24 },
  backRow: { flexDirection: 'row', alignItems: 'center', alignSelf: 'flex-start', marginBottom: 24, gap: 4 },
  backT: { fontSize: 15, color: GOLD, fontWeight: '600' },
  iconCircle: { width: 72, height: 72, borderRadius: 36, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 20 },
  title: { fontSize: 24, fontWeight: '700', color: '#1A1A1A', marginBottom: 8 },
  subtitle: { fontSize: 15, color: '#888', marginBottom: 32, textAlign: 'center' },
  pinDots: { flexDirection: 'row', gap: 20, marginBottom: 32 },
  dot: { width: 18, height: 18, borderRadius: 9, borderWidth: 2, borderColor: '#DDD' },
  dotFilled: { backgroundColor: GOLD, borderColor: GOLD },
  hiddenInput: { position: 'absolute', opacity: 0, width: 1, height: 1 },
  tapArea: { paddingVertical: 12 },
  tapText: { fontSize: 13, color: '#CCC' },
  error: { fontSize: 14, color: '#DC2626', marginTop: 8 },
  btn: { backgroundColor: GOLD, paddingVertical: 16, paddingHorizontal: 48, borderRadius: 14, marginTop: 8, elevation: 4, shadowColor: GOLD, shadowOffset: { width: 0, height: 4 }, shadowOpacity: 0.25, shadowRadius: 8 },
  btnT: { fontSize: 16, fontWeight: '700', color: '#FFF' },
  btn2: { paddingVertical: 14, marginTop: 10 },
  btn2T: { fontSize: 15, color: '#888', fontWeight: '600' },
});
