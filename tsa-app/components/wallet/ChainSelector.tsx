import React from 'react';
import { View, ScrollView, TouchableOpacity, Text, StyleSheet } from 'react-native';
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
  const isCompact = size === 'compact';
  const chipPadH = isCompact ? 10 : 14;
  const chipPadV = isCompact ? 6 : 10;
  const fontSize = isCompact ? 12 : 13;

  return (
    <ScrollView horizontal showsHorizontalScrollIndicator={false} style={styles.scroll} contentContainerStyle={styles.container}>
      {showAll && onSelectAll && (
        <TouchableOpacity
          style={[
            styles.chip,
            { paddingHorizontal: chipPadH, paddingVertical: chipPadV },
            allSelected && styles.chipActive,
          ]}
          onPress={onSelectAll}
          activeOpacity={0.7}
        >
          <Text style={[styles.chipText, { fontSize }, allSelected && styles.chipTextActive]}>
            All
          </Text>
        </TouchableOpacity>
      )}
      {availableChains.map((chainKey) => {
        const chain = CHAINS[chainKey];
        if (!chain) return null;
        const active = !allSelected && selectedChain === chainKey;
        return (
          <TouchableOpacity
            key={chainKey}
            style={[
              styles.chip,
              { paddingHorizontal: chipPadH, paddingVertical: chipPadV },
              active && styles.chipActive,
            ]}
            onPress={() => onSelect(chainKey)}
            activeOpacity={0.7}
          >
            <View style={[styles.chainDot, { backgroundColor: chain.iconColor }]} />
            <Text style={[styles.chipText, { fontSize }, active && styles.chipTextActive]}>
              {chain.shortName}
            </Text>
          </TouchableOpacity>
        );
      })}
    </ScrollView>
  );
};

export default ChainSelector;

const GOLD = '#D4AF37';

const styles = StyleSheet.create({
  scroll: {
    flexGrow: 0,
  },
  container: {
    flexDirection: 'row',
    gap: 8,
  },
  chip: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    backgroundColor: '#F5F5F5',
    borderRadius: 20,
    borderWidth: 1,
    borderColor: '#F0F0F0',
  },
  chipActive: {
    backgroundColor: '#FFF8E1',
    borderColor: GOLD,
  },
  chainDot: {
    width: 8,
    height: 8,
    borderRadius: 4,
  },
  chipText: {
    fontWeight: '600',
    color: '#888',
  },
  chipTextActive: {
    color: '#1A1A1A',
    fontWeight: '700',
  },
});
