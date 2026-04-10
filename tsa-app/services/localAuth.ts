// services/localAuth.ts
// Local authentication: biometric (fingerprint/face) + PIN fallback
let LocalAuthentication: typeof import('expo-local-authentication') | null = null;
try {
  LocalAuthentication = require('expo-local-authentication');
} catch {
  // Native module not available in this build — biometric features disabled
}
import * as SecureStore from 'expo-secure-store';
import AsyncStorage from '@react-native-async-storage/async-storage';

const PIN_KEY = 'tsa-app-pin';
const BIOMETRIC_ENABLED_KEY = 'tsa-biometric-enabled';
const PIN_ENABLED_KEY = 'tsa-pin-enabled';
const LOCK_ENABLED_KEY = 'tsa-lock-enabled';

// ── Biometric support ──

export async function isBiometricAvailable(): Promise<boolean> {
  if (!LocalAuthentication) return false;
  const compatible = await LocalAuthentication.hasHardwareAsync();
  if (!compatible) return false;
  const enrolled = await LocalAuthentication.isEnrolledAsync();
  return enrolled;
}

export async function getBiometricType(): Promise<string> {
  if (!LocalAuthentication) return 'Biometric';
  const types = await LocalAuthentication.supportedAuthenticationTypesAsync();
  if (types.includes(LocalAuthentication.AuthenticationType.FACIAL_RECOGNITION)) return 'Face ID';
  if (types.includes(LocalAuthentication.AuthenticationType.FINGERPRINT)) return 'Fingerprint';
  if (types.includes(LocalAuthentication.AuthenticationType.IRIS)) return 'Iris';
  return 'Biometric';
}

export async function authenticateWithBiometric(): Promise<boolean> {
  if (!LocalAuthentication) return false;
  const result = await LocalAuthentication.authenticateAsync({
    promptMessage: 'Unlock TSA Connect',
    fallbackLabel: 'Use PIN',
    disableDeviceFallback: true,
  });
  return result.success;
}

// ── PIN ──

export async function setPin(pin: string): Promise<void> {
  await SecureStore.setItemAsync(PIN_KEY, pin);
  await AsyncStorage.setItem(PIN_ENABLED_KEY, 'true');
  await AsyncStorage.setItem(LOCK_ENABLED_KEY, 'true');
}

export async function verifyPin(pin: string): Promise<boolean> {
  const stored = await SecureStore.getItemAsync(PIN_KEY);
  return stored === pin;
}

export async function hasPin(): Promise<boolean> {
  const stored = await SecureStore.getItemAsync(PIN_KEY);
  return !!stored;
}

export async function removePin(): Promise<void> {
  await SecureStore.deleteItemAsync(PIN_KEY);
  await AsyncStorage.removeItem(PIN_ENABLED_KEY);
}

// ── Lock settings ──

export async function isLockEnabled(): Promise<boolean> {
  const v = await AsyncStorage.getItem(LOCK_ENABLED_KEY);
  return v === 'true';
}

export async function setLockEnabled(enabled: boolean): Promise<void> {
  await AsyncStorage.setItem(LOCK_ENABLED_KEY, enabled ? 'true' : 'false');
}

export async function isBiometricEnabled(): Promise<boolean> {
  const v = await AsyncStorage.getItem(BIOMETRIC_ENABLED_KEY);
  return v === 'true';
}

export async function setBiometricEnabled(enabled: boolean): Promise<void> {
  await AsyncStorage.setItem(BIOMETRIC_ENABLED_KEY, enabled ? 'true' : 'false');
}

// ── Combined auth check ──

export interface LockState {
  isLocked: boolean;
  hasBiometric: boolean;
  hasPin: boolean;
  biometricType: string;
}

export async function getLockState(): Promise<LockState> {
  const lockEnabled = await isLockEnabled();
  if (!lockEnabled) {
    return { isLocked: false, hasBiometric: false, hasPin: false, biometricType: '' };
  }

  const [bioAvailable, bioEnabled, pinSet, bioType] = await Promise.all([
    isBiometricAvailable(),
    isBiometricEnabled(),
    hasPin(),
    getBiometricType(),
  ]);

  return {
    isLocked: true,
    hasBiometric: bioAvailable && bioEnabled,
    hasPin: pinSet,
    biometricType: bioType,
  };
}

// ── Refresh token (SecureStore – survives soft logout) ──

const REFRESH_TOKEN_KEY = 'tsa-refresh-token';

export async function storeRefreshToken(token: string): Promise<void> {
  await SecureStore.setItemAsync(REFRESH_TOKEN_KEY, token);
}

export async function getRefreshToken(): Promise<string | null> {
  return SecureStore.getItemAsync(REFRESH_TOKEN_KEY);
}

export async function clearRefreshToken(): Promise<void> {
  await SecureStore.deleteItemAsync(REFRESH_TOKEN_KEY);
}

export async function hasRefreshToken(): Promise<boolean> {
  const token = await SecureStore.getItemAsync(REFRESH_TOKEN_KEY);
  return !!token;
}

// ── Sensitive operation authorization ──

export async function authorizeWithBiometric(): Promise<boolean> {
  const state = await getLockState();
  if (state.hasBiometric) {
    return authenticateWithBiometric();
  }
  return false;
}
