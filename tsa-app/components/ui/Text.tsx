// Typed typography primitive. Variant -> type scale; weight -> Inter family.
// Caps font scaling at 1.3x. AmountText renders locale-formatted numerals (tabular).
import React from 'react';
import { Text as RNText, TextProps as RNTextProps, TextStyle } from 'react-native';
import {
  ColorTokens,
  FontWeightToken,
  TypeVariant,
  formatFiat,
  formatNumber,
  useTheme,
} from '@/theme';

export interface TextProps extends RNTextProps {
  variant?: TypeVariant;
  color?: keyof ColorTokens;
  weight?: FontWeightToken;
  align?: TextStyle['textAlign'];
  numeric?: boolean;
}

export const Text: React.FC<TextProps> = ({
  variant = 'body',
  color = 'textPrimary',
  weight,
  align,
  numeric,
  style,
  children,
  ...rest
}) => {
  const { type, fonts, colors } = useTheme();
  const t = type[variant];
  const w = weight ?? (t.weight as FontWeightToken);
  const letterSpacing = 'letterSpacing' in t ? (t as { letterSpacing?: number }).letterSpacing : undefined;

  return (
    <RNText
      maxFontSizeMultiplier={1.3}
      style={[
        {
          fontFamily: fonts[w],
          fontSize: t.fontSize,
          lineHeight: t.lineHeight,
          letterSpacing,
          color: colors[color],
          textAlign: align,
          ...(numeric ? { fontVariant: ['tabular-nums'] as TextStyle['fontVariant'] } : null),
        },
        style,
      ]}
      {...rest}
    >
      {children}
    </RNText>
  );
};

export interface AmountTextProps extends Omit<TextProps, 'children' | 'numeric'> {
  value: number | string;
  /** Fiat ISO code -> renders with currency symbol; omit for a plain grouped number. */
  currency?: string;
  withCode?: boolean;
  locale?: string;
  minimumFractionDigits?: number;
  maximumFractionDigits?: number;
}

/** Bold, tabular-numeral money/number display (the "confident numbers" of the system). */
export const AmountText: React.FC<AmountTextProps> = ({
  value,
  currency,
  withCode,
  locale,
  minimumFractionDigits,
  maximumFractionDigits,
  variant = 'title3',
  weight = '700',
  ...rest
}) => {
  const num = typeof value === 'string' ? Number(value) : value;
  const text = currency
    ? formatFiat(value, currency, { withCode, locale, minimumFractionDigits, maximumFractionDigits })
    : formatNumber(isFinite(num) ? num : 0, { locale, minimumFractionDigits, maximumFractionDigits });
  return (
    <Text variant={variant} weight={weight} numeric {...rest}>
      {text}
    </Text>
  );
};
