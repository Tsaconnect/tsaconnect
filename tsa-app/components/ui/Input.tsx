// Text field with label, helper, error, optional counter + adornments. Focus ring
// uses the theme focusRing token; error state overrides border + helper color.
import React, { useState } from 'react';
import { TextInput, TextInputProps, View, ViewStyle } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';

export interface InputProps extends Omit<TextInputProps, 'style'> {
  label?: string;
  helper?: string;
  error?: string;
  /** true => show "len/maxLength"; number => show "len/number". */
  counter?: boolean | number;
  leftAdornment?: React.ReactNode;
  rightAdornment?: React.ReactNode;
  containerStyle?: ViewStyle;
  inputStyle?: ViewStyle;
}

export const Input: React.FC<InputProps> = ({
  label,
  helper,
  error,
  counter,
  leftAdornment,
  rightAdornment,
  containerStyle,
  inputStyle,
  value,
  maxLength,
  onFocus,
  onBlur,
  ...rest
}) => {
  const { colors, radius, spacing, fonts, type } = useTheme();
  const [focused, setFocused] = useState(false);

  const borderColor = error ? colors.danger : focused ? colors.accent : colors.border;
  const counterMax = typeof counter === 'number' ? counter : maxLength;
  const showCounter = !!counter && !!counterMax;

  return (
    <View style={containerStyle}>
      {label && (
        <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
          {label}
        </Text>
      )}
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          minHeight: 48,
          paddingHorizontal: spacing.md,
          borderRadius: radius.md,
          borderWidth: 1.5,
          borderColor,
          backgroundColor: colors.surface,
          ...(focused && !error ? { shadowColor: colors.focusRing, shadowOpacity: 1, shadowRadius: 4, shadowOffset: { width: 0, height: 0 } } : null),
        }}
      >
        {leftAdornment}
        <TextInput
          value={value}
          maxLength={maxLength}
          placeholderTextColor={colors.textMuted}
          onFocus={(e) => {
            setFocused(true);
            onFocus?.(e);
          }}
          onBlur={(e) => {
            setFocused(false);
            onBlur?.(e);
          }}
          style={[
            {
              flex: 1,
              paddingVertical: spacing.md,
              color: colors.textPrimary,
              fontFamily: fonts['400'],
              fontSize: type.body.fontSize,
            },
            inputStyle as object,
          ]}
          {...rest}
        />
        {rightAdornment}
      </View>
      {(helper || error || showCounter) && (
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', marginTop: spacing.xs }}>
          <Text variant="caption" color={error ? 'danger' : 'textMuted'} style={{ flex: 1 }}>
            {error || helper || ''}
          </Text>
          {showCounter && (
            <Text variant="caption" color="textMuted">
              {`${(value?.length ?? 0)}/${counterMax}`}
            </Text>
          )}
        </View>
      )}
    </View>
  );
};
