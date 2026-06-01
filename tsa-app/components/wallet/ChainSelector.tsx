// components/wallet/ChainSelector.tsx
//
// Horizontal chain-filter chip row for the wallet (send / receive / home).
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). Visual/structural restyle only — the props interface, exports,
// chain list (CHAINS registry), and selection logic are preserved verbatim. The
// per-chain dot color still comes from each chain's `iconColor` (registry data,
// not styling).
import React from 'react';
import { ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { PressableScale, Text } from '@/components/ui';
import { CHAINS, type ChainKey } from '../../constants/chains';

interface ChainSelectorProps {
  availableChains: ChainKey[];
  selectedChain: ChainKey;
  onSelect: (chain: ChainKey) => void;
  showAll?: boolean;
  onSelectAll?: () => void;
  allSelected?: boolean;
  size?: 'compact' | 'normal';
}

const ChainSelector = ({
  availableChains,
  selectedChain,
  onSelect,
  showAll = false,
  onSelectAll,
  allSelected = false,
  size = 'normal',
}: ChainSelectorProps) => {
  const { colors, spacing, radius } = useTheme();
  const isCompact = size === 'compact';
  const chipPadH = isCompact ? spacing.md : spacing.lg;
  const chipPadV = isCompact ? spacing.xs : spacing.sm;

  const chipStyle = (active: boolean) => ({
    flexDirection: 'row' as const,
    alignItems: 'center' as const,
    gap: spacing.xs,
    paddingHorizontal: chipPadH,
    paddingVertical: chipPadV,
    borderRadius: radius.pill,
    borderWidth: 1,
    borderColor: active ? colors.accent : colors.border,
    backgroundColor: active ? colors.accentMuted : colors.surface,
  });

  return (
    <ScrollView
      horizontal
      showsHorizontalScrollIndicator={false}
      style={{ flexGrow: 0 }}
      contentContainerStyle={{ flexDirection: 'row', gap: spacing.sm }}
    >
      {showAll && onSelectAll && (
        <PressableScale style={chipStyle(allSelected)} onPress={onSelectAll}>
          <Text
            variant={isCompact ? 'caption' : 'bodySm'}
            weight={allSelected ? '700' : '600'}
            style={{ color: allSelected ? colors.textPrimary : colors.textMuted }}
          >
            All
          </Text>
        </PressableScale>
      )}
      {availableChains.map((chainKey) => {
        const chain = CHAINS[chainKey];
        if (!chain) return null;
        const active = !allSelected && selectedChain === chainKey;
        return (
          <PressableScale
            key={chainKey}
            style={chipStyle(active)}
            onPress={() => onSelect(chainKey)}
          >
            <View
              style={{
                width: 8,
                height: 8,
                borderRadius: radius.pill,
                backgroundColor: chain.iconColor,
              }}
            />
            <Text
              variant={isCompact ? 'caption' : 'bodySm'}
              weight={active ? '700' : '600'}
              style={{ color: active ? colors.textPrimary : colors.textMuted }}
            >
              {chain.shortName}
            </Text>
          </PressableScale>
        );
      })}
    </ScrollView>
  );
};

export default ChainSelector;
