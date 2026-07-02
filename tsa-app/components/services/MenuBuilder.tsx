// components/services/MenuBuilder.tsx
//
// Order-type menu builder. Merchants add/remove menu items (name, description,
// price, optional image). Purely a controlled list — the parent owns the state
// and persists each item via createServiceMenuItem after the product exists.
//
// TSA Signature design system only (@/theme + @/components/ui).
import React from 'react';
import { Image, View } from 'react-native';
import * as ImagePicker from 'expo-image-picker';
import { useTheme } from '@/theme';
import { Button, Card, Icon, IconButton, Input, PressableScale, Text } from '@/components/ui';

// A menu item as edited in the form. `imageUri` is a local file URI (from the
// picker) until upload; `imageUrl` is a persisted remote URL (edit prefetch).
export interface MenuDraft {
  id?: string; // server id for an already-saved item; absent for a new draft
  name: string;
  description: string;
  priceUsd: string; // kept as string for the text input
  imageUri?: string;
  imageUrl?: string;
}

export const emptyMenuDraft = (): MenuDraft => ({
  name: '',
  description: '',
  priceUsd: '',
});

interface MenuBuilderProps {
  items: MenuDraft[];
  onChange: (items: MenuDraft[]) => void;
}

const MenuBuilder: React.FC<MenuBuilderProps> = ({ items, onChange }) => {
  const { colors, spacing, radius } = useTheme();

  const update = (index: number, patch: Partial<MenuDraft>) => {
    onChange(items.map((it, i) => (i === index ? { ...it, ...patch } : it)));
  };

  const addItem = () => onChange([...items, emptyMenuDraft()]);

  const removeItem = (index: number) => onChange(items.filter((_, i) => i !== index));

  const pickImage = async (index: number) => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== 'granted') return;
    const result = await ImagePicker.launchImageLibraryAsync({
      aspect: [4, 3],
      quality: 0.8,
      allowsMultipleSelection: false,
    });
    if (!result.canceled && result.assets[0]) {
      update(index, { imageUri: result.assets[0].uri });
    }
  };

  return (
    <View style={{ gap: spacing.lg }}>
      {items.map((item, index) => {
        const preview = item.imageUri || item.imageUrl;
        return (
          <Card key={index} padding="lg" style={{ gap: spacing.md }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text variant="bodyMed" weight="700">
                Item {index + 1}
              </Text>
              <IconButton
                icon="trash"
                color={colors.danger}
                accessibilityLabel={`Remove item ${index + 1}`}
                onPress={() => removeItem(index)}
              />
            </View>

            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <PressableScale onPress={() => pickImage(index)}>
                {preview ? (
                  <Image
                    source={{ uri: preview }}
                    style={{
                      width: 72,
                      height: 72,
                      borderRadius: radius.md,
                      backgroundColor: colors.surfaceAlt,
                    }}
                  />
                ) : (
                  <View
                    style={{
                      width: 72,
                      height: 72,
                      borderRadius: radius.md,
                      borderWidth: 1.5,
                      borderStyle: 'dashed',
                      borderColor: colors.accentMuted,
                      alignItems: 'center',
                      justifyContent: 'center',
                      backgroundColor: colors.surface,
                    }}
                  >
                    <Icon name="image" size={22} color={colors.accent} />
                  </View>
                )}
              </PressableScale>

              <View style={{ flex: 1, gap: spacing.md }}>
                <Input
                  label="Name *"
                  value={item.name}
                  onChangeText={(t) => update(index, { name: t })}
                  placeholder="e.g. Jollof Rice"
                />
                <Input
                  label="Price ($)"
                  value={item.priceUsd}
                  onChangeText={(t) => update(index, { priceUsd: t })}
                  keyboardType="decimal-pad"
                  placeholder="0.00"
                />
              </View>
            </View>

            <Input
              label="Description"
              value={item.description}
              onChangeText={(t) => update(index, { description: t })}
              placeholder="Short description (optional)"
              multiline
              numberOfLines={2}
              textAlignVertical="top"
              inputStyle={{ minHeight: 56 }}
            />
          </Card>
        );
      })}

      <Button title="Add item" variant="secondary" leftIcon="plus" onPress={addItem} />
    </View>
  );
};

export default MenuBuilder;
