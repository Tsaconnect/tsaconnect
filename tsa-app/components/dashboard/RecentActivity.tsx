// components/dashboard/RecentActivity.tsx
import React from 'react';
import { View, Text, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { Transaction } from '@/services/walletApi';

interface RecentActivityProps {
  transactions: Transaction[];
  isValuesHidden: boolean;
}

const TX_ICONS: Record<string, { icon: string; color: string }> = {
  send: { icon: 'arrow-upward', color: '#F87171' },
  receive: { icon: 'arrow-downward', color: '#4ADE80' },
  swap: { icon: 'swap-horiz', color: '#60A5FA' },
};

function truncateAddress(addr: string): string {
  if (!addr || addr.length < 10) return addr || '';
  return `${addr.slice(0, 6)}...${addr.slice(-4)}`;
}

function deriveDescription(tx: Transaction): string {
  const type = tx.type?.toLowerCase() || 'transfer';
  if (type.includes('send') || type.includes('transfer')) {
    return `Sent ${tx.amount} ${tx.tokenSymbol} to ${truncateAddress(tx.toAddress)}`;
  }
  if (type.includes('receive')) {
    return `Received ${tx.amount} ${tx.tokenSymbol} from ${truncateAddress(tx.fromAddress)}`;
  }
  if (type.includes('swap')) {
    return `Swapped ${tx.amount} ${tx.tokenSymbol}`;
  }
  return `${tx.type} ${tx.amount} ${tx.tokenSymbol}`;
}

function formatTime(dateStr: string): string {
  const date = new Date(dateStr);
  const now = new Date();
  const diffMs = now.getTime() - date.getTime();
  const diffMin = Math.floor(diffMs / 60000);
  if (diffMin < 1) return 'Just now';
  if (diffMin < 60) return `${diffMin}m ago`;
  const diffHr = Math.floor(diffMin / 60);
  if (diffHr < 24) return `${diffHr}h ago`;
  const diffDay = Math.floor(diffHr / 24);
  if (diffDay < 7) return `${diffDay}d ago`;
  return date.toLocaleDateString();
}

export const RecentActivity: React.FC<RecentActivityProps> = ({ transactions, isValuesHidden }) => {
  const txType = (tx: Transaction) => {
    const t = tx.type?.toLowerCase() || '';
    if (t.includes('send') || t.includes('transfer')) return 'send';
    if (t.includes('receive')) return 'receive';
    if (t.includes('swap')) return 'swap';
    return 'send';
  };

  return (
    <View style={styles.container}>
      <View style={styles.header}>
        <Text style={styles.headerTitle}>Recent Activity</Text>
      </View>

      {transactions.length === 0 ? (
        <View style={styles.empty}>
          <Icon name="receipt-long" size={32} color="#DDD" />
          <Text style={styles.emptyText}>No recent transactions</Text>
        </View>
      ) : (
        transactions.map((tx, index) => {
          const typeInfo = TX_ICONS[txType(tx)] || TX_ICONS.send;
          const isLast = index === transactions.length - 1;
          return (
            <View key={tx.id || tx.txHash || index} style={[styles.row, !isLast && styles.rowBorder]}>
              <View style={[styles.txIcon, { backgroundColor: `${typeInfo.color}15` }]}>
                <Icon name={typeInfo.icon} size={18} color={typeInfo.color} />
              </View>
              <View style={styles.txInfo}>
                <Text style={styles.txDesc} numberOfLines={1}>
                  {isValuesHidden ? `${tx.type || 'Transaction'} ••••` : deriveDescription(tx)}
                </Text>
                <Text style={styles.txTime}>{formatTime(tx.createdAt)}</Text>
              </View>
              <View style={styles.txStatus}>
                <Text style={[
                  styles.statusText,
                  tx.status === 'confirmed' && styles.statusConfirmed,
                  tx.status === 'pending' && styles.statusPending,
                  tx.status === 'failed' && styles.statusFailed,
                ]}>
                  {tx.status}
                </Text>
              </View>
            </View>
          );
        })
      )}
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
  empty: { alignItems: 'center', paddingVertical: 28 },
  emptyText: { fontSize: 13, color: '#BBB', marginTop: 8 },
  row: {
    flexDirection: 'row',
    alignItems: 'center',
    paddingHorizontal: 16,
    paddingVertical: 12,
    gap: 12,
  },
  rowBorder: { borderBottomWidth: 1, borderBottomColor: '#F5F5F5' },
  txIcon: {
    width: 36,
    height: 36,
    borderRadius: 18,
    justifyContent: 'center',
    alignItems: 'center',
  },
  txInfo: { flex: 1 },
  txDesc: { fontSize: 13, fontWeight: '500', color: '#1A1A1A' },
  txTime: { fontSize: 11, color: '#AAA', marginTop: 2 },
  txStatus: {},
  statusText: { fontSize: 11, fontWeight: '500', color: '#888', textTransform: 'capitalize' },
  statusConfirmed: { color: '#16A34A' },
  statusPending: { color: '#D97706' },
  statusFailed: { color: '#DC2626' },
});

export default RecentActivity;
