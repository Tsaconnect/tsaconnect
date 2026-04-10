import React, { useState, useEffect } from 'react';
import {
  Modal, View, Text, TouchableOpacity, TextInput,
  ActivityIndicator, StyleSheet,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import {
  isBiometricAvailable, isBiometricEnabled, getBiometricType,
  authenticateWithBiometric, hasPin, verifyPin,
} from '../../services/localAuth';
import api from '../services/api';

const COLORS = {
  primary: '#D4AF37',
  gold: '#FFD700',
  dark: '#1a1a1a',
  gray: '#666',
  lightGray: '#e0e0e0',
  danger: '#DC3545',
  white: '#fff',
  overlay: 'rgba(0,0,0,0.5)',
};

interface AuthorizationModalProps {
  visible: boolean;
  title?: string;
  description?: string;
  onAuthorized: () => void;
  onCancel: () => void;
}

type AuthMethod = 'choose' | 'biometric' | 'pin' | 'otp';

export default function AuthorizationModal({
  visible,
  title = 'Authorize',
  description,
  onAuthorized,
  onCancel,
}: AuthorizationModalProps) {
  const [method, setMethod] = useState<AuthMethod>('choose');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');

  const [bioAvailable, setBioAvailable] = useState(false);
  const [bioType, setBioType] = useState('Fingerprint');

  const [pinAvailable, setPinAvailable] = useState(false);
  const [pinValue, setPinValue] = useState('');
  const [pinAttempts, setPinAttempts] = useState(0);

  const [otpValue, setOtpValue] = useState('');
  const [otpSent, setOtpSent] = useState(false);
  const [otpCountdown, setOtpCountdown] = useState(0);

  useEffect(() => {
    if (!visible) {
      setMethod('choose');
      setError('');
      setPinValue('');
      setOtpValue('');
      setOtpSent(false);
      setPinAttempts(0);
      setLoading(false);
      return;
    }
    (async () => {
      const [bioOk, bioOn, pinOk, type] = await Promise.all([
        isBiometricAvailable(),
        isBiometricEnabled(),
        hasPin(),
        getBiometricType(),
      ]);
      setBioAvailable(bioOk && bioOn);
      setPinAvailable(pinOk);
      setBioType(type);
    })();
  }, [visible]);

  useEffect(() => {
    if (otpCountdown <= 0) return;
    const timer = setTimeout(() => setOtpCountdown(otpCountdown - 1), 1000);
    return () => clearTimeout(timer);
  }, [otpCountdown]);

  const handleBiometric = async () => {
    setError('');
    const success = await authenticateWithBiometric();
    if (success) {
      onAuthorized();
    } else {
      setError('Biometric verification failed. Try another method.');
    }
  };

  const handlePinSubmit = async (pin: string) => {
    setError('');
    setLoading(true);
    const valid = await verifyPin(pin);
    setLoading(false);
    if (valid) {
      onAuthorized();
    } else {
      const attempts = pinAttempts + 1;
      setPinAttempts(attempts);
      setPinValue('');
      if (attempts >= 3) {
        setError('Too many PIN attempts. Use OTP instead.');
        setMethod('otp');
        setPinAttempts(0);
      } else {
        setError(`Incorrect PIN. ${3 - attempts} attempts remaining.`);
      }
    }
  };

  const handleSendOtp = async () => {
    setError('');
    setLoading(true);
    const result = await api.sendOtp();
    setLoading(false);
    if (result.success) {
      setOtpSent(true);
      setOtpCountdown(60);
    } else {
      setError(result.message || 'Failed to send OTP');
    }
  };

  const handleVerifyOtp = async () => {
    setError('');
    setLoading(true);
    const result = await api.verifyOtp(otpValue);
    setLoading(false);
    if (result.success) {
      onAuthorized();
    } else {
      setOtpValue('');
      setError(result.message || 'Invalid OTP. Please try again.');
    }
  };

  const renderChoose = () => (
    <View style={styles.methodContainer}>
      {bioAvailable && (
        <TouchableOpacity style={styles.methodButton} onPress={handleBiometric}>
          <Ionicons
            name={bioType === 'Face ID' ? 'scan' : 'finger-print'}
            size={28}
            color={COLORS.primary}
          />
          <Text style={styles.methodText}>Use {bioType}</Text>
        </TouchableOpacity>
      )}
      {pinAvailable && (
        <TouchableOpacity style={styles.methodButton} onPress={() => setMethod('pin')}>
          <Ionicons name="keypad" size={28} color={COLORS.primary} />
          <Text style={styles.methodText}>Use PIN</Text>
        </TouchableOpacity>
      )}
      <TouchableOpacity
        style={styles.methodButton}
        onPress={() => { setMethod('otp'); handleSendOtp(); }}
      >
        <Ionicons name="mail" size={28} color={COLORS.primary} />
        <Text style={styles.methodText}>Use Email OTP</Text>
      </TouchableOpacity>
    </View>
  );

  const renderPin = () => (
    <View style={styles.inputContainer}>
      <Text style={styles.inputLabel}>Enter your 4-digit PIN</Text>
      <TextInput
        style={styles.pinInput}
        value={pinValue}
        onChangeText={(text) => {
          setPinValue(text);
          if (text.length === 4) handlePinSubmit(text);
        }}
        keyboardType="number-pad"
        maxLength={4}
        secureTextEntry
        autoFocus
        placeholder="••••"
        placeholderTextColor="#ccc"
      />
      {loading && <ActivityIndicator color={COLORS.primary} style={{ marginTop: 12 }} />}
      <TouchableOpacity onPress={() => setMethod('choose')} style={styles.backButton}>
        <Text style={styles.backText}>Try another method</Text>
      </TouchableOpacity>
    </View>
  );

  const renderOtp = () => (
    <View style={styles.inputContainer}>
      {!otpSent ? (
        <>
          <Text style={styles.inputLabel}>Sending verification code to your email...</Text>
          <ActivityIndicator color={COLORS.primary} />
        </>
      ) : (
        <>
          <Text style={styles.inputLabel}>Enter the 6-digit code sent to your email</Text>
          <TextInput
            style={styles.otpInput}
            value={otpValue}
            onChangeText={setOtpValue}
            keyboardType="number-pad"
            maxLength={6}
            autoFocus
            placeholder="000000"
            placeholderTextColor="#ccc"
          />
          <TouchableOpacity
            style={[styles.submitBtn, otpValue.length !== 6 && styles.submitBtnDisabled]}
            onPress={handleVerifyOtp}
            disabled={otpValue.length !== 6 || loading}
          >
            {loading ? (
              <ActivityIndicator color="#fff" size="small" />
            ) : (
              <Text style={styles.submitBtnText}>Verify</Text>
            )}
          </TouchableOpacity>
          <TouchableOpacity
            onPress={handleSendOtp}
            disabled={otpCountdown > 0 || loading}
            style={styles.resendBtn}
          >
            <Text style={[styles.resendText, otpCountdown > 0 && { color: '#ccc' }]}>
              {otpCountdown > 0 ? `Resend in ${otpCountdown}s` : 'Resend Code'}
            </Text>
          </TouchableOpacity>
        </>
      )}
      <TouchableOpacity onPress={() => setMethod('choose')} style={styles.backButton}>
        <Text style={styles.backText}>Try another method</Text>
      </TouchableOpacity>
    </View>
  );

  return (
    <Modal visible={visible} transparent animationType="slide" onRequestClose={onCancel}>
      <View style={styles.overlay}>
        <View style={styles.modal}>
          <View style={styles.header}>
            <Text style={styles.title}>{title}</Text>
            <TouchableOpacity onPress={onCancel} hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}>
              <Ionicons name="close" size={24} color={COLORS.gray} />
            </TouchableOpacity>
          </View>

          {description && <Text style={styles.description}>{description}</Text>}

          {error !== '' && (
            <View style={styles.errorContainer}>
              <Ionicons name="alert-circle" size={16} color={COLORS.danger} />
              <Text style={styles.errorText}>{error}</Text>
            </View>
          )}

          {method === 'choose' && renderChoose()}
          {method === 'pin' && renderPin()}
          {method === 'otp' && renderOtp()}
        </View>
      </View>
    </Modal>
  );
}

const styles = StyleSheet.create({
  overlay: {
    flex: 1,
    backgroundColor: COLORS.overlay,
    justifyContent: 'flex-end',
  },
  modal: {
    backgroundColor: COLORS.white,
    borderTopLeftRadius: 24,
    borderTopRightRadius: 24,
    padding: 24,
    paddingBottom: 40,
    minHeight: 300,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  title: {
    fontSize: 20,
    fontWeight: '700',
    color: COLORS.dark,
  },
  description: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 20,
  },
  errorContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff5f5',
    borderRadius: 8,
    padding: 10,
    marginBottom: 16,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 13,
    marginLeft: 8,
    flex: 1,
  },
  methodContainer: {
    gap: 12,
    marginTop: 8,
  },
  methodButton: {
    flexDirection: 'row',
    alignItems: 'center',
    padding: 16,
    borderRadius: 14,
    borderWidth: 1.5,
    borderColor: COLORS.lightGray,
    gap: 14,
  },
  methodText: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
  },
  inputContainer: {
    alignItems: 'center',
    marginTop: 8,
  },
  inputLabel: {
    fontSize: 14,
    color: COLORS.gray,
    marginBottom: 16,
    textAlign: 'center',
  },
  pinInput: {
    fontSize: 32,
    fontWeight: '700',
    textAlign: 'center',
    letterSpacing: 16,
    width: 200,
    borderBottomWidth: 2,
    borderBottomColor: COLORS.primary,
    paddingVertical: 8,
    color: COLORS.dark,
  },
  otpInput: {
    fontSize: 24,
    fontWeight: '600',
    textAlign: 'center',
    letterSpacing: 8,
    width: 240,
    borderBottomWidth: 2,
    borderBottomColor: COLORS.primary,
    paddingVertical: 8,
    color: COLORS.dark,
  },
  submitBtn: {
    backgroundColor: COLORS.primary,
    paddingVertical: 14,
    paddingHorizontal: 48,
    borderRadius: 12,
    marginTop: 20,
    alignItems: 'center',
  },
  submitBtnDisabled: {
    opacity: 0.5,
  },
  submitBtnText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '600',
  },
  resendBtn: {
    marginTop: 16,
  },
  resendText: {
    fontSize: 14,
    fontWeight: '500',
    color: COLORS.primary,
  },
  backButton: {
    marginTop: 20,
  },
  backText: {
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '500',
  },
});
