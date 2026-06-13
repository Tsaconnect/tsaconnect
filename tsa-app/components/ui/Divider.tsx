import React from 'react';
import { View } from 'react-native';
import { SpacingKey, useTheme } from '@/theme';

export interface DividerProps {
  vertical?: boolean;
  spacing?: SpacingKey;
  inset?: number;
}

export const Divider: React.FC<DividerProps> = ({ vertical, spacing, inset = 0 }) => {
  const { colors, spacing: s } = useTheme();
  const gap = spacing ? s[spacing] : 0;
  if (vertical) {
    return <View style={{ width: 1, alignSelf: 'stretch', backgroundColor: colors.hairline, marginHorizontal: gap }} />;
  }
  return (
    <View
      style={{ height: 1, backgroundColor: colors.hairline, marginVertical: gap, marginStart: inset }}
    />
  );
};
