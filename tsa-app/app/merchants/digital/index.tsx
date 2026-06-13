// app/merchants/digital/index.tsx
//
// Merchant — create a digital product/asset (title, description, price,
// category, download URL). Restyled onto the TSA Signature design system
// (@/theme tokens + @/components/ui primitives). The form state, the
// required-field validation, and the create/Alert flow are all preserved
// verbatim — this is a visual/structural restyle only.
import React, { useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Button, Card, Input, Text } from '@/components/ui';

const DigitalAssetCreationScreen = () => {
  const { colors, spacing } = useTheme();
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        keyboardShouldPersistTaps="handled"
      >
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>
          Create Digital Asset
        </Text>

        <Card padding="lg" style={{ gap: spacing.lg }}>
          <Input
            label="Asset Title *"
            placeholder="e.g. Masterclass E-book"
            value={formData.title}
            onChangeText={(text) => setFormData({ ...formData, title: text })}
          />

          <Input
            label="Description"
            placeholder="Provide details about your digital product..."
            multiline
            numberOfLines={4}
            value={formData.description}
            onChangeText={(text) => setFormData({ ...formData, description: text })}
            inputStyle={{ height: 100, textAlignVertical: 'top' }}
          />

          <View style={{ flexDirection: 'row', gap: spacing.md }}>
            <Input
              label="Price (USD) *"
              placeholder="0.00"
              keyboardType="numeric"
              value={formData.price}
              onChangeText={(text) => setFormData({ ...formData, price: text })}
              containerStyle={{ flex: 1 }}
            />
            <Input
              label="Category"
              placeholder="e.g. Education"
              value={formData.category}
              onChangeText={(text) => setFormData({ ...formData, category: text })}
              containerStyle={{ flex: 1 }}
            />
          </View>

          <Input
            label="Asset Download URL *"
            placeholder="https://storage.provider.com/your-file.zip"
            autoCapitalize="none"
            value={formData.downloadUrl}
            onChangeText={(text) => setFormData({ ...formData, downloadUrl: text })}
            helper="The link provided to customers after purchase."
          />
        </Card>

        <Button
          title="Create Digital Asset"
          onPress={handleCreateAsset}
          size="lg"
          style={{ marginTop: spacing.xl }}
        />
      </ScrollView>
    </SafeAreaView>
  );
};

export default DigitalAssetCreationScreen;
