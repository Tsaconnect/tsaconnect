// A label with an (i) that reveals a short explanation in place: for the
// detail a screen should keep within reach without printing it for everyone,
// every time.
import React, { useState } from 'react';
import { Pressable, View } from 'react-native';
import { ColorTokens, useTheme } from '@/theme';
import { Text, type TextProps } from './Text';
import { Icon } from './Icon';

export interface InfoTipProps {
  label: string;
  tip: string;
  variant?: TextProps['variant'];
  weight?: TextProps['weight'];
  color?: keyof ColorTokens;
}

export const InfoTip: React.FC<InfoTipProps> = ({
  label,
  tip,
  variant = 'bodySm',
  weight,
  color = 'textSecondary',
}) => {
  const { colors, spacing } = useTheme();
  const [open, setOpen] = useState(false);
  return (
    <View style={{ flexShrink: 1 }}>
      <Pressable
        onPress={() => setOpen((o) => !o)}
        accessibilityRole="button"
        accessibilityLabel={`${label}. More information`}
        accessibilityState={{ expanded: open }}
        hitSlop={8}
        style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
      >
        <Text variant={variant} weight={weight} color={color}>
          {label}
        </Text>
        <Icon name="info" size={14} color={colors.textMuted} />
      </Pressable>
      {open ? (
        <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
          {tip}
        </Text>
      ) : null}
    </View>
  );
};
