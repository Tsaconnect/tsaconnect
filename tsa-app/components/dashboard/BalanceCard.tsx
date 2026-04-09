// components/dashboard/BalanceCard.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';

interface BalanceCardProps {
  totalUsdValue: number;
  dailyChange: number;
  isValuesHidden: boolean;
  onToggleVisibility: () => void;
}

const QUICK_ACTIONS = [
  { key: 'buy', label: 'Buy', icon: 'shopping-bag', route: '/(dashboard)/(tabs)/(marketplace)' },
  { key: 'services', label: 'Services', icon: 'miscellaneous-services', route: '/(dashboard)/(tabs)/(services)/serviceshome' },
  { key: 'trade', label: 'Trade', icon: 'trending-up', route: '/easyswap' },
  { key: 'wallet', label: 'Wallet', icon: 'account-balance-wallet', route: '/tokenization' },
] as const;

export const BalanceCard: React.FC<BalanceCardProps> = ({
  totalUsdValue,
  dailyChange,
  isValuesHidden,
  onToggleVisibility,
}) => {
  const changeColor = dailyChange >= 0 ? '#4ADE80' : '#F87171';
  const changePrefix = dailyChange >= 0 ? '+' : '';

  return (
    <View style={styles.card}>
      <View style={styles.header}>
        <Text style={styles.label}>Total Portfolio</Text>
        <Pressable onPress={onToggleVisibility} hitSlop={8}>
          <Icon
            name={isValuesHidden ? 'visibility-off' : 'visibility'}
            size={20}
            color="rgba(255,255,255,0.4)"
          />
        </Pressable>
      </View>

      <Text style={styles.balance}>
        {isValuesHidden
          ? '••••••'
          : `$${totalUsdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
      </Text>

      {dailyChange !== 0 && (
        <View style={styles.changeRow}>
          <Icon
            name={dailyChange >= 0 ? 'trending-up' : 'trending-down'}
            size={14}
            color={changeColor}
          />
          <Text style={[styles.changeText, { color: changeColor }]}>
            {changePrefix}${Math.abs(dailyChange).toFixed(2)} today
          </Text>
        </View>
      )}

      <View style={styles.actionsRow}>
        {QUICK_ACTIONS.map((action) => (
          <Pressable
            key={action.key}
            style={({ pressed }) => [styles.actionButton, pressed && styles.actionPressed]}
            onPress={() => router.push(action.route as any)}
          >
            <View style={styles.actionIcon}>
              <Icon name={action.icon} size={20} color="#D4AF37" />
            </View>
            <Text style={styles.actionLabel}>{action.label}</Text>
          </Pressable>
        ))}
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  card: {
    marginHorizontal: 16,
    marginTop: 8,
    borderRadius: 16,
    padding: 20,
    backgroundColor: '#1A1A1A',
  },
  header: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    alignItems: 'center',
    marginBottom: 8,
  },
  label: { fontSize: 13, color: 'rgba(255,255,255,0.6)' },
  balance: { fontSize: 28, fontWeight: '800', color: '#FFFFFF', marginBottom: 4 },
  changeRow: { flexDirection: 'row', alignItems: 'center', gap: 4, marginBottom: 4 },
  changeText: { fontSize: 12, fontWeight: '500' },
  actionsRow: {
    flexDirection: 'row',
    justifyContent: 'space-around',
    marginTop: 18,
    paddingTop: 14,
    borderTopWidth: 1,
    borderTopColor: 'rgba(255,255,255,0.1)',
  },
  actionButton: { alignItems: 'center', minWidth: 56 },
  actionPressed: { opacity: 0.7 },
  actionIcon: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: 'rgba(212,175,55,0.15)',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 6,
  },
  actionLabel: { fontSize: 11, color: 'rgba(255,255,255,0.7)', fontWeight: '500' },
});

export default BalanceCard;
