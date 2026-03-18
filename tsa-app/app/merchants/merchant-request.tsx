import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  ScrollView,
  StyleSheet,
  ActivityIndicator,
  Alert,
} from 'react-native';
import { Stack, router } from 'expo-router';
import { COLORS } from '../../constants';
import { useAuth } from '../../AuthContext/AuthContext';
import { getMyMerchantRequest, submitMerchantRequest } from '../../components/services/api';

const BUSINESS_TYPES = [
  { label: 'General Products', value: 'general_products' },
  { label: 'Digital Products', value: 'digital_products' },
  { label: 'P2P Merchant', value: 'p2p_merchant' },
  { label: 'Service Provider', value: 'service_provider' },
];

export default function MerchantRequestScreen() {
  const { user } = useAuth();
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
  const [phone, setPhone] = useState(user?.phoneNumber || '');
  const [registrationNumber, setRegistrationNumber] = useState('');

  useEffect(() => {
    fetchExistingRequest();
  }, []);

  const fetchExistingRequest = async () => {
    setLoading(true);
    try {
      const res = await getMyMerchantRequest();
      if (res.success && res.data) {
        setExistingRequest(res.data);
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
      <View style={styles.center}>
        <Stack.Screen options={{ title: 'Merchant Application' }} />
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  if (existingRequest) {
    const { status } = existingRequest;

    if (status === 'pending') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={styles.statusCard}>
            <Text style={styles.statusEmoji}>&#9203;</Text>
            <Text style={styles.statusTitle}>Application Under Review</Text>
            <Text style={styles.statusText}>
              Your merchant application for "{existingRequest.businessName}" is being reviewed. We'll update your status soon.
            </Text>
          </View>
        </View>
      );
    }

    if (status === 'approved') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={[styles.statusCard, { borderColor: '#22c55e' }]}>
            <Text style={styles.statusEmoji}>&#9989;</Text>
            <Text style={styles.statusTitle}>You're a Merchant!</Text>
            <Text style={styles.statusText}>
              Your application has been approved. You now have access to the merchant portal.
            </Text>
            <TouchableOpacity
              style={[styles.submitButton, { backgroundColor: '#22c55e' }]}
              onPress={() => router.push('/merchants/dashboard')}
            >
              <Text style={styles.submitButtonText}>Go to Merchant Dashboard</Text>
            </TouchableOpacity>
          </View>
        </View>
      );
    }

    if (status === 'rejected') {
      return (
        <View style={styles.container}>
          <Stack.Screen options={{ title: 'Merchant Application' }} />
          <View style={[styles.statusCard, { borderColor: '#ef4444' }]}>
            <Text style={styles.statusEmoji}>&#10060;</Text>
            <Text style={styles.statusTitle}>Application Rejected</Text>
            <Text style={styles.statusText}>
              Reason: {existingRequest.adminNote || 'No reason provided'}
            </Text>
            <TouchableOpacity
              style={styles.submitButton}
              onPress={() => {
                setExistingRequest(null);
                setBusinessName('');
                setBusinessDescription('');
                setAddress('');
                setCity('');
                setState('');
                setCountry('');
                setRegistrationNumber('');
              }}
            >
              <Text style={styles.submitButtonText}>Apply Again</Text>
            </TouchableOpacity>
          </View>
        </View>
      );
    }
  }

  return (
    <ScrollView style={styles.container}>
      <Stack.Screen options={{ title: 'Merchant Application' }} />
      <Text style={styles.heading}>Become a Merchant</Text>
      <Text style={styles.subheading}>Fill in your business details to apply</Text>

      <Text style={styles.label}>Business Type *</Text>
      <View style={styles.pickerRow}>
        {BUSINESS_TYPES.map((bt) => (
          <TouchableOpacity
            key={bt.value}
            style={[styles.pickerOption, businessType === bt.value && styles.pickerOptionSelected]}
            onPress={() => setBusinessType(bt.value)}
          >
            <Text style={[styles.pickerText, businessType === bt.value && styles.pickerTextSelected]}>
              {bt.label}
            </Text>
          </TouchableOpacity>
        ))}
      </View>

      <Text style={styles.label}>Business Name *</Text>
      <TextInput style={styles.input} value={businessName} onChangeText={setBusinessName} placeholder="e.g. Ojay Electronics" />

      <Text style={styles.label}>Business Description</Text>
      <TextInput style={[styles.input, { height: 80 }]} value={businessDescription} onChangeText={setBusinessDescription} placeholder="Describe your business..." multiline />

      <Text style={styles.label}>Address *</Text>
      <TextInput style={styles.input} value={address} onChangeText={setAddress} placeholder="Street address" />

      <Text style={styles.label}>City *</Text>
      <TextInput style={styles.input} value={city} onChangeText={setCity} placeholder="City" />

      <Text style={styles.label}>State *</Text>
      <TextInput style={styles.input} value={state} onChangeText={setState} placeholder="State" />

      <Text style={styles.label}>Country *</Text>
      <TextInput style={styles.input} value={country} onChangeText={setCountry} placeholder="Country" />

      <Text style={styles.label}>Phone *</Text>
      <TextInput style={styles.input} value={phone} onChangeText={setPhone} placeholder="+234..." keyboardType="phone-pad" />

      <Text style={styles.label}>Registration Number (optional)</Text>
      <TextInput style={styles.input} value={registrationNumber} onChangeText={setRegistrationNumber} placeholder="e.g. RC-123456" />

      <TouchableOpacity style={styles.submitButton} onPress={handleSubmit} disabled={submitting}>
        {submitting ? (
          <ActivityIndicator color="#fff" />
        ) : (
          <Text style={styles.submitButtonText}>Submit Application</Text>
        )}
      </TouchableOpacity>

      <View style={{ height: 40 }} />
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#f5f5f5', padding: 16 },
  center: { flex: 1, justifyContent: 'center', alignItems: 'center' },
  heading: { fontSize: 22, fontWeight: '700', color: '#1e293b', marginBottom: 4 },
  subheading: { fontSize: 14, color: '#64748b', marginBottom: 20 },
  label: { fontSize: 13, fontWeight: '600', color: '#475569', marginTop: 12, marginBottom: 4 },
  input: {
    backgroundColor: '#fff',
    borderWidth: 1,
    borderColor: '#e2e8f0',
    borderRadius: 8,
    padding: 12,
    fontSize: 14,
    color: '#1e293b',
  },
  pickerRow: { flexDirection: 'row', flexWrap: 'wrap', gap: 8 },
  pickerOption: {
    paddingHorizontal: 12,
    paddingVertical: 8,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: '#e2e8f0',
    backgroundColor: '#fff',
  },
  pickerOptionSelected: { borderColor: '#9b795fff', backgroundColor: '#fef3c7' },
  pickerText: { fontSize: 13, color: '#64748b' },
  pickerTextSelected: { color: '#9b795fff', fontWeight: '600' },
  submitButton: {
    backgroundColor: '#9b795fff',
    borderRadius: 8,
    padding: 14,
    alignItems: 'center',
    marginTop: 20,
  },
  submitButtonText: { color: '#fff', fontSize: 16, fontWeight: '600' },
  statusCard: {
    backgroundColor: '#fff',
    borderRadius: 12,
    padding: 24,
    alignItems: 'center',
    borderWidth: 1,
    borderColor: '#f59e0b',
    marginTop: 40,
  },
  statusEmoji: { fontSize: 48, marginBottom: 12 },
  statusTitle: { fontSize: 20, fontWeight: '700', color: '#1e293b', marginBottom: 8 },
  statusText: { fontSize: 14, color: '#64748b', textAlign: 'center', lineHeight: 20 },
});
