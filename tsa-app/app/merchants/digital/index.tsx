import React, { useState } from 'react';
import { View, Text, TextInput, TouchableOpacity, StyleSheet, ScrollView, Alert } from 'react-native';

const DigitalAssetCreationScreen = () => {
  const [formData, setFormData] = useState({
    title: '',
    description: '',
    price: '',
    downloadUrl: '',
    category: '',
  });

  const handleCreateAsset = () => {
    if (!formData.title || !formData.price || !formData.downloadUrl) {
      Alert.alert('Error', 'Please fill in all required fields');
      return;
    }
    // Logic to save the digital asset would go here
    Alert.alert('Success', 'Digital asset created successfully!');
  };

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Text style={styles.header}>Create Digital Asset</Text>
      
      <View style={styles.formGroup}>
        <Text style={styles.label}>Asset Title *</Text>
        <TextInput
          style={styles.input}
          placeholder="e.g. Masterclass E-book"
          value={formData.title}
          onChangeText={(text) => setFormData({ ...formData, title: text })}
        />
      </View>

      <View style={styles.formGroup}>
        <Text style={styles.label}>Description</Text>
        <TextInput
          style={[styles.input, styles.textArea]}
          placeholder="Provide details about your digital product..."
          multiline
          numberOfLines={4}
          value={formData.description}
          onChangeText={(text) => setFormData({ ...formData, description: text })}
        />
      </View>

      <View style={styles.row}>
        <View style={[styles.formGroup, { flex: 1, marginRight: 10 }]}>
          <Text style={styles.label}>Price (USD) *</Text>
          <TextInput
            style={styles.input}
            placeholder="0.00"
            keyboardType="numeric"
            value={formData.price}
            onChangeText={(text) => setFormData({ ...formData, price: text })}
          />
        </View>
        <View style={[styles.formGroup, { flex: 1 }]}>
          <Text style={styles.label}>Category</Text>
          <TextInput
            style={styles.input}
            placeholder="e.g. Education"
            value={formData.category}
            onChangeText={(text) => setFormData({ ...formData, category: text })}
          />
        </View>
      </View>

      <View style={styles.formGroup}>
        <Text style={styles.label}>Asset Download URL *</Text>
        <TextInput
          style={styles.input}
          placeholder="https://storage.provider.com/your-file.zip"
          autoCapitalize="none"
          value={formData.downloadUrl}
          onChangeText={(text) => setFormData({ ...formData, downloadUrl: text })}
        />
        <Text style={styles.helperText}>The link provided to customers after purchase.</Text>
      </View>

      <TouchableOpacity style={styles.submitButton} onPress={handleCreateAsset}>
        <Text style={styles.submitButtonText}>Create Digital Asset</Text>
      </TouchableOpacity>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    padding: 20,
    backgroundColor: '#F9FAFB',
    flexGrow: 1,
  },
  header: {
    fontSize: 24,
    fontWeight: 'bold',
    marginBottom: 24,
    color: '#111827',
  },
  formGroup: {
    marginBottom: 16,
  },
  row: {
    flexDirection: 'row',
  },
  label: {
    fontSize: 14,
    fontWeight: '600',
    color: '#374151',
    marginBottom: 6,
  },
  input: {
    backgroundColor: '#FFFFFF',
    borderWidth: 1,
    borderColor: '#D1D5DB',
    borderRadius: 8,
    padding: 12,
    fontSize: 16,
    color: '#111827',
  },
  textArea: {
    height: 100,
    textAlignVertical: 'top',
  },
  helperText: {
    fontSize: 12,
    color: '#6B7280',
    marginTop: 4,
  },
  submitButton: {
    backgroundColor: '#2563EB',
    paddingVertical: 14,
    borderRadius: 8,
    alignItems: 'center',
    marginTop: 24,
  },
  submitButtonText: {
    color: '#FFFFFF',
    fontSize: 16,
    fontWeight: 'bold',
  },
});

export default DigitalAssetCreationScreen;
