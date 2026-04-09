// app/lock.tsx
// Lock screen — biometric prompt + PIN fallback
import React, { useState, useEffect, useRef } from 'react';
import {
  View, Text, StyleSheet, TouchableOpacity, TextInput, ActivityIndicator, Pressable,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import { router } from 'expo-router';
import {
  authenticateWithBiometric, verifyPin, getLockState, getBiometricType,
} from '@/services/localAuth';

const GOLD = '#D4AF37';

const LockScreen = () => {
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
      <View style={s.container}>
        <ActivityIndicator size="large" color={GOLD} />
      </View>
    );
  }

  return (
    <View style={s.container}>
      <View style={s.content}>
        {/* Logo / Icon */}
        <View style={s.iconCircle}>
          <Ionicons name="lock-closed" size={32} color={GOLD} />
        </View>
        <Text style={s.title}>TSA Connect</Text>
        <Text style={s.subtitle}>
          {showPinInput ? 'Enter your PIN to unlock' : `Use ${biometricType} to unlock`}
        </Text>

        {/* PIN input */}
        {showPinInput && (
          <View style={s.pinSection}>
            {/* PIN dots */}
            <View style={s.pinDots}>
              {[0, 1, 2, 3].map(i => (
                <View key={i} style={[s.dot, pin.length > i && s.dotFilled]} />
              ))}
            </View>

            <TextInput
              ref={pinRef}
              style={s.hiddenInput}
              value={pin}
              onChangeText={handlePinChange}
              keyboardType="number-pad"
              maxLength={4}
              secureTextEntry
              autoFocus
            />

            {error ? <Text style={s.error}>{error}</Text> : null}
          </View>
        )}

        {/* Biometric retry button */}
        {!showPinInput && (
          <TouchableOpacity style={s.bioButton} onPress={tryBiometric}>
            <Ionicons
              name={biometricType === 'Face ID' ? 'scan' : 'finger-print'}
              size={48}
              color={GOLD}
            />
            <Text style={s.bioText}>Tap to unlock with {biometricType}</Text>
          </TouchableOpacity>
        )}

        {/* Switch between biometric and PIN */}
        <Pressable
          style={s.switchButton}
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
          <Text style={s.switchText}>
            {showPinInput ? `Use ${biometricType} instead` : 'Use PIN instead'}
          </Text>
        </Pressable>
      </View>
    </View>
  );
};

export default LockScreen;

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA', justifyContent: 'center', alignItems: 'center' },
  content: { alignItems: 'center', padding: 24, width: '100%' },
  iconCircle: { width: 72, height: 72, borderRadius: 36, backgroundColor: '#FFF8E1', justifyContent: 'center', alignItems: 'center', marginBottom: 20 },
  title: { fontSize: 24, fontWeight: '700', color: '#1A1A1A', marginBottom: 8 },
  subtitle: { fontSize: 15, color: '#888', marginBottom: 32 },
  pinSection: { alignItems: 'center', width: '100%' },
  pinDots: { flexDirection: 'row', gap: 16, marginBottom: 24 },
  dot: { width: 16, height: 16, borderRadius: 8, borderWidth: 2, borderColor: '#DDD', backgroundColor: 'transparent' },
  dotFilled: { backgroundColor: GOLD, borderColor: GOLD },
  hiddenInput: { position: 'absolute', opacity: 0, width: 1, height: 1 },
  error: { fontSize: 14, color: '#DC2626', marginTop: 8 },
  bioButton: { alignItems: 'center', paddingVertical: 24 },
  bioText: { fontSize: 15, color: '#888', marginTop: 12 },
  switchButton: { marginTop: 24, paddingVertical: 12 },
  switchText: { fontSize: 14, color: GOLD, fontWeight: '600' },
});
