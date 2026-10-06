// components/ramp/FeeLines.tsx
//
// Every provider fee as its own line, tagged with the provider (spec
// 2026-10-03 §14): a rate never looks high without a stated reason. Shared by
// the buy screen and the Instant Transfer order screen.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';

/** "2474.8" -> "2,474.80": a fiat amount with thousands separators and 2 dp,
 *  the way the app writes naira everywhere (PF13). Non-numbers pass through. */
export function groupThousands(value: string): string {
  const n = Number(value);
  if (!value || !Number.isFinite(n)) return value;
  return n.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 });
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
            {f.ngn ? `−${f.token} ${token} (≈ ${symbol}${groupThousands(f.ngn)})` : `−${f.token} ${token}`}
          </Text>
        </View>
      ))}
    </View>
  );
};

export default FeeLines;
