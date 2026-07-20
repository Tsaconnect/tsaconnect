// components/dashboard/AssetList.tsx
//
// The dashboard's per-symbol balance list. Migrated onto the TSA Signature
// design system — every balance read (`asset.balance.toFixed(2)`), the
// `formatPrice` USD-value render, and the hide-values masking are preserved
// verbatim; only presentation moved to @/theme tokens + @/components/ui.
import React from 'react';
import { View } from 'react-native';
import { Asset } from '@/components/services/api';
import { useCurrency } from '@/contexts/CurrencyContext';
import { useTheme } from '@/theme';
import { Avatar, Divider, PressableScale, Surface, Text } from '@/components/ui';

interface AssetListProps {
  assets: Asset[];
  isValuesHidden: boolean;
  /** Tapping a row opens the pair/chain picker. Rows stay inert when omitted. */
  onAssetPress?: (symbol: string) => void;
}

export const AssetList: React.FC<AssetListProps> = ({ assets, isValuesHidden, onAssetPress }) => {
  const { formatPrice } = useCurrency();
  const { spacing } = useTheme();

  return (
    <Surface elevation="sm" style={{ marginTop: spacing.lg, overflow: 'hidden' }}>
      <View style={{ paddingHorizontal: spacing.lg, paddingVertical: spacing.md }}>
        <Text variant="bodySm" weight="700">
          Assets
        </Text>
      </View>
      <Divider />

      {assets.map((asset, index) => {
        const isLast = index === assets.length - 1;
        const RowContainer: React.ElementType = onAssetPress ? PressableScale : View;
        return (
          <View key={asset._id || asset.id || asset.symbol}>
            <RowContainer
              {...(onAssetPress
                ? {
                    onPress: () => onAssetPress(asset.symbol),
                    accessibilityRole: 'button' as const,
                    accessibilityLabel: `View ${asset.symbol} pairs and chains`,
                  }
                : {})}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.md,
                paddingHorizontal: spacing.lg,
                paddingVertical: spacing.md,
              }}
            >
              <Avatar name={asset.symbol} size={36} />
              <View style={{ flex: 1 }}>
                <Text variant="bodySm" weight="600">
                  {asset.symbol}
                </Text>
                <Text variant="caption" color="textMuted">
                  {asset.name}
                </Text>
              </View>
              <View style={{ alignItems: 'flex-end' }}>
                <Text variant="bodySm" weight="700" numeric>
                  {isValuesHidden ? '••••' : asset.balance.toFixed(2)}
                </Text>
                <Text variant="caption" color="textMuted" numeric>
                  {isValuesHidden ? '••••' : formatPrice(asset.usdValue)}
                </Text>
              </View>
            </RowContainer>
            {!isLast && <Divider inset={spacing.lg} />}
          </View>
        );
      })}
    </Surface>
  );
};

export default AssetList;
