// components/dashboard/AssetList.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import { Asset } from '@/components/services/api';
import { useCurrency } from '@/contexts/CurrencyContext';

const ASSET_COLORS: Record<string, string[]> = {
  MCGP: ['#D4AF37', '#B8941F'],
  USDC: ['#2775CA', '#1A5BA8'],
  USDT: ['#26A17B', '#1A8A66'],
};

interface AssetListProps {
  assets: Asset[];
  isValuesHidden: boolean;
}

export const AssetList: React.FC<AssetListProps> = ({ assets, isValuesHidden }) => {
  const { formatPrice } = useCurrency();
  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Assets</Text>
      </View>

      {assets.map((asset, index) => {
        const colors = ASSET_COLORS[asset.symbol] || ['#666', '#444'];
        const isLast = index === assets.length - 1;
        return (
          <View
            key={asset._id || asset.id || asset.symbol}
            style={[styles.row, !isLast && styles.rowBorder]}
          >
            <View style={[styles.avatar, { backgroundColor: colors[0] }]}>
              <Text style={styles.avatarText}>{asset.symbol.charAt(0)}</Text>
            </View>
            <View style={styles.info}>
              <Text style={styles.symbol}>{asset.symbol}</Text>
              <Text style={styles.name}>{asset.name}</Text>
            </View>
            <View style={styles.balanceCol}>
              <Text style={styles.balanceText}>
                {isValuesHidden ? '••••' : asset.balance.toFixed(2)}
              </Text>
              <Text style={styles.usdText}>
                {isValuesHidden ? '••••' : formatPrice(asset.usdValue)}
              </Text>
            </View>
          </View>
        );
      })}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    marginHorizontal: 16,
    marginTop: 14,
    backgroundColor: '#FFF',
    borderRadius: 14,
    overflow: 'hidden',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    borderBottomWidth: 1,
    borderBottomColor: '#F0F0F0',
  },
  headerTitle: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    gap: 12,
  },
  rowBorder: { borderBottomWidth: 1, borderBottomColor: '#F5F5F5' },
  avatar: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
  },
  avatarText: { color: '#FFF', fontSize: 14, fontWeight: '700' },
  info: { flex: 1 },
  symbol: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  name: { fontSize: 11, color: '#888', marginTop: 1 },
  balanceCol: { alignItems: 'flex-end' },
  balanceText: { fontSize: 14, fontWeight: '700', color: '#1A1A1A' },
  usdText: { fontSize: 11, color: '#888', marginTop: 1 },
});

export default AssetList;
