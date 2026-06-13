// Tap-to-copy value (bank account numbers, escrow address). Uses expo-clipboard
// (already in the build) + a success toast.
import React from 'react';
import { View, ViewStyle } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon } from './Icon';
import { useToast } from './Toast';

export interface CopyableFieldProps {
  value: string;
  label?: string;
  /** What to show (defaults to value; use for masked/formatted display). */
  display?: string;
  toastMessage?: string;
  style?: ViewStyle;
}

export const CopyableField: React.FC<CopyableFieldProps> = ({
  value,
  label,
  display,
  toastMessage = 'Copied',
  style,
}) => {
  const { colors, radius, spacing } = useTheme();
  const toast = useToast();

  const onCopy = async () => {
    try {
      await Clipboard.setStringAsync(value);
      toast.success(toastMessage);
    } catch {
      toast.error('Could not copy');
    }
  };

  return (
    <PressableScale
      onPress={onCopy}
      accessibilityRole="button"
      accessibilityLabel={`Copy ${label ?? value}`}
      style={[
        {
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          padding: spacing.md,
          borderRadius: radius.md,
          backgroundColor: colors.surfaceAlt,
        },
        style,
      ]}
    >
      <View style={{ flex: 1 }}>
        {label && (
          <Text variant="caption" color="textMuted">
            {label}
          </Text>
        )}
        <Text variant="bodyMed" weight="600" numeric>
          {display ?? value}
        </Text>
      </View>
      <Icon name="copy" size={18} color={colors.accent} />
    </PressableScale>
  );
};
