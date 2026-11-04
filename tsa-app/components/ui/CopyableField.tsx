// Tap-to-copy value (bank account numbers, escrow address). Uses expo-clipboard
// (already in the build): a success toast, but a failed copy stays inline so
// the user knows to enter the value by hand.
import React, { useState } from 'react';
import { View, ViewStyle } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { PressableScale } from './PressableScale';
import { Text } from './Text';
import { Icon } from './Icon';
import { useToast } from './Toast';
import { InlineMessage } from './InlineMessage';

export interface CopyableFieldProps {
  value: string;
  label?: string;
  /** What to show (defaults to value; use for masked/formatted display). */
  display?: string;
  toastMessage?: string;
  /** Applies to the outer container (the field plus any copy error below it). */
  style?: ViewStyle;
}

/** Copies a value with the field's toast, keeping a failure inline for the
 *  caller to show (InlineMessage), so the user knows to enter it by hand. */
export function useCopy(toastMessage = 'Copied') {
  const toast = useToast();
  const [copyError, setCopyError] = useState<string | null>(null);
  const copy = async (value: string) => {
    setCopyError(null);
    try {
      await Clipboard.setStringAsync(value);
      toast.success(toastMessage);
    } catch {
      setCopyError('Could not copy. Enter it by hand instead.');
    }
  };
  return { copy, copyError, clearCopyError: () => setCopyError(null) };
}

export const CopyableField: React.FC<CopyableFieldProps> = ({
  value,
  label,
  display,
  toastMessage = 'Copied',
  style,
}) => {
  const { colors, radius, spacing } = useTheme();
  const { copy, copyError, clearCopyError } = useCopy(toastMessage);
  const onCopy = () => copy(value);

  return (
    <View style={[{ gap: spacing.sm }, style]}>
      <PressableScale
        onPress={onCopy}
        accessibilityRole="button"
        accessibilityLabel={`Copy ${label ?? value}`}
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          padding: spacing.md,
          borderRadius: radius.md,
          backgroundColor: colors.surfaceAlt,
        }}
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
      <InlineMessage message={copyError} onDismiss={clearCopyError} />
    </View>
  );
};
