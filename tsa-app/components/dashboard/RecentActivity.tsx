// components/dashboard/RecentActivity.tsx
//
// Recent wallet transactions list. Migrated onto the TSA Signature design
// system — the type→icon mapping, description derivation, time/address
// formatting, and confirmed/pending/failed status colors are all preserved
// verbatim; only presentation moved to @/theme tokens + @/components/ui.
import React from 'react';
import { View } from 'react-native';
import { Transaction } from '@/services/walletApi';
import { useTheme } from '@/theme';
import { Divider, EmptyState, Icon, Surface, Text } from '@/components/ui';
import type { IconName } from '@/components/ui';

const ARROW_UP: IconName = 'arrowUp';
const ARROW_DOWN: IconName = 'arrowDown';

interface RecentActivityProps {
  transactions: Transaction[];
  isValuesHidden: boolean;
}

const TX_ICONS: Record<string, { icon: IconName; tone: 'danger' | 'success' | 'info' }> = {
  send: { icon: ARROW_UP, tone: 'danger' },
  receive: { icon: ARROW_DOWN, tone: 'success' },
  swap: { icon: 'swap', tone: 'info' },
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
  const { colors, spacing } = useTheme();

  const txType = (tx: Transaction) => {
    const t = tx.type?.toLowerCase() || '';
    if (t.includes('send') || t.includes('transfer')) return 'send';
    if (t.includes('receive')) return 'receive';
    if (t.includes('swap')) return 'swap';
    return 'send';
  };

  const statusColor = (status: Transaction['status']): keyof typeof colors => {
    if (status === 'confirmed') return 'success';
    if (status === 'pending') return 'warning';
    if (status === 'failed') return 'danger';
    return 'textMuted';
  };

  return (
    <Surface elevation="sm" style={{ marginTop: spacing.lg, overflow: 'hidden' }}>
      <View style={{ paddingHorizontal: spacing.lg, paddingVertical: spacing.md }}>
        <Text variant="bodySm" weight="700">
          Recent Activity
        </Text>
      </View>
      <Divider />

      {transactions.length === 0 ? (
        <View style={{ paddingVertical: spacing.lg }}>
          <EmptyState icon="empty" title="No recent transactions" />
        </View>
      ) : (
        transactions.map((tx, index) => {
          const typeInfo = TX_ICONS[txType(tx)] || TX_ICONS.send;
          const toneColor = colors[typeInfo.tone];
          const toneBg = colors[`${typeInfo.tone}Bg` as keyof typeof colors];
          const isLast = index === transactions.length - 1;
          return (
            <View key={tx.id || tx.txHash || index}>
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingHorizontal: spacing.lg,
                  paddingVertical: spacing.md,
                }}
              >
                <View
                  style={{
                    width: 36,
                    height: 36,
                    borderRadius: 18,
                    backgroundColor: toneBg,
                    justifyContent: 'center',
                    alignItems: 'center',
                  }}
                >
                  <Icon name={typeInfo.icon} size={18} color={toneColor} />
                </View>
                <View style={{ flex: 1 }}>
                  <Text variant="bodySm" weight="600" numberOfLines={1}>
                    {isValuesHidden ? `${tx.type || 'Transaction'} ••••` : deriveDescription(tx)}
                  </Text>
                  <Text variant="caption" color="textMuted">
                    {formatTime(tx.createdAt)}
                  </Text>
                </View>
                <Text
                  variant="caption"
                  weight="600"
                  color={statusColor(tx.status)}
                  style={{ textTransform: 'capitalize' }}
                >
                  {tx.status}
                </Text>
              </View>
              {!isLast && <Divider inset={spacing.lg} />}
            </View>
          );
        })
      )}
    </Surface>
  );
};

export default RecentActivity;
