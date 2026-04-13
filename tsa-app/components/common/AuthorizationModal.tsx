import React, { useEffect, useState } from 'react';
import {
  ActivityIndicator,
  Modal,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import {
  authenticateWithBiometric,
  getBiometricType,
  hasPin,
  isBiometricAvailable,
  verifyPin,
} from '../../services/localAuth';

const COLORS = {
  primary: '#D4AF37',
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

type AuthMethod = 'choose' | 'pin';

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

      {!bioAvailable && !pinAvailable && (
        <View style={styles.emptyState}>
          <Ionicons name="shield-checkmark-outline" size={28} color={COLORS.primary} />
          <Text style={styles.emptyTitle}>Authorization unavailable</Text>
          <Text style={styles.emptyText}>
            Set up app PIN or biometrics in Settings before sending funds.
          </Text>
        </View>
      )}
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
          if (text.length === 4) {
            handlePinSubmit(text);
          }
        }}
        keyboardType="number-pad"
        maxLength={4}
        secureTextEntry
        autoFocus
        placeholder="...."
        placeholderTextColor="#ccc"
      />
      {loading && <ActivityIndicator color={COLORS.primary} style={styles.loader} />}
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
            <TouchableOpacity
              onPress={onCancel}
              hitSlop={{ top: 10, bottom: 10, left: 10, right: 10 }}
            >
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
  emptyState: {
    alignItems: 'center',
    borderWidth: 1.5,
    borderColor: COLORS.lightGray,
    borderRadius: 14,
    padding: 20,
    gap: 8,
  },
  emptyTitle: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
  },
  emptyText: {
    fontSize: 14,
    color: COLORS.gray,
    textAlign: 'center',
    lineHeight: 20,
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
  loader: {
    marginTop: 12,
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
