// components/common/QrPanel.tsx
//
// A scannable QR code: dark-on-light at a fixed size, on its own white panel,
// so it scans in both light and dark themes. Extracted from the Receive screen
// so a payment link's QR reads the same. `children` overlay the centre (the
// Receive screen's token logo) — pass ecl "H" when they cover modules.
import React from 'react';
import { View } from 'react-native';
import QRCode from 'react-native-qrcode-svg';
import { useTheme } from '@/theme';

// QR codes must stay high-contrast dark-on-light to remain scannable regardless of
// the active theme, so these two values are intentionally not theme-driven.
export const QR_FG = '#16201F';
export const QR_BG = '#FFFFFF';

export interface QrPanelProps {
  value: string;
  size?: number;
  ecl?: 'L' | 'M' | 'Q' | 'H';
  children?: React.ReactNode;
}

export function QrPanel({ value, size = 200, ecl = 'M', children }: QrPanelProps) {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        padding: spacing.sm,
        backgroundColor: QR_BG,
        borderRadius: radius.lg,
        borderWidth: 1,
        borderColor: colors.hairline,
      }}
    >
      <QRCode value={value} size={size} color={QR_FG} backgroundColor={QR_BG} ecl={ecl} />
      {children ? (
        <View
          pointerEvents="none"
          style={{ position: 'absolute', top: 0, bottom: 0, left: 0, right: 0, alignItems: 'center', justifyContent: 'center' }}
        >
          {children}
        </View>
      ) : null}
    </View>
  );
}
