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
  /** Who charges it — kept for attribution, never shown by name. */
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

/** Third-party names never reach the screen: a fee says "provider", and a
 *  label stored before that rule ("Flutterwave conversion fee") is rewritten. */
function feeLabel(f: FeeLine): string {
  if (!f.provider) return f.label;
  const name = f.provider.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return f.label
    .replace(new RegExp(`^${name}\\b`, 'i'), 'Provider')
    .replace(new RegExp(`\\b${name}\\b`, 'gi'), 'provider');
}

/** "via provider", unless the label already says provider. */
function providerTag(f: FeeLine, label: string): string | null {
  if (!f.provider) return null;
  return /provider/i.test(label) ? null : 'via provider';
}

export const FeeLines: React.FC<FeeLinesProps> = ({ fees, token, symbol }) => {
  const { colors, spacing } = useTheme();
  if (!fees.length) return null;
  return (
    <View
      testID="ramp-fee-lines"
      style={{
        marginTop: spacing.md,
        paddingTop: spacing.sm,
        borderTopWidth: 1,
        borderTopColor: colors.border,
        gap: spacing.sm,
      }}
    >
      {fees.map((f) => {
        const label = feeLabel(f);
        const tag = providerTag(f, label);
        return (
          <View
            key={`${f.label}|${f.token}`}
            style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', gap: spacing.md }}
          >
            <View style={{ flex: 1 }}>
              <Text variant="caption" style={{ color: colors.textSecondary }}>
                {label}
              </Text>
              {tag ? (
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  {tag}
                </Text>
              ) : null}
            </View>
            <View style={{ alignItems: 'flex-end', flexShrink: 0 }}>
              <Text variant="caption" weight="600" style={{ color: colors.textPrimary }}>
                {`−${f.token} ${token}`}
              </Text>
              {f.ngn ? (
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  {`≈ ${fiatText(f.ngn, symbol)}`}
                </Text>
              ) : null}
            </View>
          </View>
        );
      })}
    </View>
  );
};

export default FeeLines;
