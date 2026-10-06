// components/it/ConversionProgress.tsx
//
// A converted Instant Transfer buy (spec §15.10): the purchase pays for a
// USDC leg; what she receives is the conversion. Composes what already exists
// — the converted-order hops and stepper, the promise/estimate formatters, and
// the converted-order screen for tracking. No conversion card existed to reuse.
import React from 'react';
import { View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Stepper, Text } from '@/components/ui';
import { formatEstimate, formatPromisedAmount, paycrestConversionHops } from '@/services/paycrestApi';
import { getChainDisplay, type ChainKey } from '@/constants/chains';
import type { ITConversion } from '@/services/itApi';

export interface ConversionProgressProps {
  conversion: ITConversion;
  /** Her transfer has been received: the conversion can be tracked. */
  paid: boolean;
}

export const ConversionProgress: React.FC<ConversionProgressProps> = ({ conversion, paid }) => {
  const { colors, spacing } = useTheme();
  const chain = getChainDisplay(conversion.requestedChain as ChainKey)?.name ?? conversion.requestedChain;
  const promised = formatPromisedAmount(conversion);
  // formatEstimate already carries the token symbol; a promise does not.
  const amount = promised ? `at least ${promised} ${conversion.requestedToken}` : (formatEstimate(conversion) ?? conversion.requestedToken);
  return (
    <View testID="it-conversion" style={{ marginTop: spacing.md, gap: spacing.sm }}>
      <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
        {`You receive ${amount} on ${chain}`}
      </Text>
      <Text variant="caption" style={{ color: colors.textMuted }}>
        Converted and delivered to your wallet after your transfer arrives.
      </Text>
      <Stepper steps={paycrestConversionHops(conversion.status, conversion.holder)} />
      {paid ? (
        <View testID="it-conversion-track">
          <Button
            title="Track delivery"
            variant="secondary"
            onPress={() => router.push(`/wallet/paycrest/order/${conversion.orderId}` as never)}
          />
        </View>
      ) : null}
    </View>
  );
};

export default ConversionProgress;
