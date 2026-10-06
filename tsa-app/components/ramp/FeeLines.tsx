// components/ramp/FeeLines.tsx
//
// Every provider fee as its own line, tagged with the provider (spec
// 2026-10-03 §14): a rate never looks high without a stated reason. Shared by
// the buy screen and the Instant Transfer order screen.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { formatNaira } from '@/utils/stableSellQuote';
import { formatFiat } from '@/services/p2p';

/** A fiat amount the way the app writes it (PF13): naira through the shared
 *  string-maths formatter ("₦2,474.80"), any other currency through formatFiat. */
export function fiatText(value: string, symbol: string): string {
  return symbol === '₦' ? formatNaira(value) : `${symbol}${formatFiat(value)}`;
}

export interface FeeLine {
  label: string;
  /** The provider tag ("Flutterwave"). */
  provider?: string;
  /** The fee in tokens, decimal string. */
  token: string;
  /** Its naira value, 2 dp. */
  ngn?: string;
}

export interface FeeLinesProps {
  fees: FeeLine[];
  /** The token symbol ("USDC"). */
  token: string;
  /** The fiat symbol ("₦"). */
  symbol: string;
}

export const FeeLines: React.FC<FeeLinesProps> = ({ fees, token, symbol }) => {
  const { colors, spacing } = useTheme();
  if (!fees.length) return null;
  return (
    <View testID="ramp-fee-lines" style={{ marginTop: spacing.sm, gap: spacing.xs }}>
      {fees.map((f) => (
        <View
          key={`${f.label}|${f.token}`}
          style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', gap: spacing.sm }}
        >
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, flexShrink: 1 }}>
            <Text variant="caption" style={{ color: colors.textSecondary }}>
              {f.label}
            </Text>
            {f.provider ? (
              <Text variant="caption" weight="600" style={{ color: colors.textMuted }}>
                {f.provider}
              </Text>
            ) : null}
          </View>
          <Text variant="caption" style={{ color: colors.textSecondary }}>
            {f.ngn ? `−${f.token} ${token} (≈ ${fiatText(f.ngn, symbol)})` : `−${f.token} ${token}`}
          </Text>
        </View>
      ))}
    </View>
  );
};

export default FeeLines;
