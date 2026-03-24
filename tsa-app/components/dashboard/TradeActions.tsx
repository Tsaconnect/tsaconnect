// components/dashboard/TradeActions.tsx
import React from 'react';
import { View, Text, Pressable, StyleSheet } from 'react-native';
import Icon from 'react-native-vector-icons/MaterialIcons';
import { router } from 'expo-router';

const TRADE_ITEMS = [
  {
    key: 'product',
    title: 'Buy Product',
    subtitle: 'Browse marketplace',
    icon: 'shopping-bag',
    color: '#D4AF37',
    bg: 'rgba(212,175,55,0.1)',
    route: '/products',
  },
  {
    key: 'services',
    title: 'Order Services',
    subtitle: 'Find service providers',
    icon: 'miscellaneous-services',
    color: '#3B82F6',
    bg: 'rgba(59,130,246,0.1)',
    route: '/serviceshome',
  },
  {
    key: 'trade',
    title: 'Trade & Earn',
    subtitle: 'Swap and earn rewards',
    icon: 'trending-up',
    color: '#16A34A',
    bg: 'rgba(22,163,74,0.1)',
    route: '/trade',
  },
] as const;

export const TradeActions: React.FC = () => {
  return (
    <View style={styles.container}>
      <Text style={styles.sectionTitle}>Trade Now</Text>
      {TRADE_ITEMS.map((item) => (
        <Pressable
          key={item.key}
          style={({ pressed }) => [styles.card, pressed && styles.cardPressed]}
          onPress={() => router.push(item.route as any)}
        >
          <View style={[styles.iconWrap, { backgroundColor: item.bg }]}>
            <Icon name={item.icon} size={24} color={item.color} />
          </View>
          <View style={styles.cardInfo}>
            <Text style={styles.cardTitle}>{item.title}</Text>
            <Text style={styles.cardSubtitle}>{item.subtitle}</Text>
          </View>
          <Icon name="chevron-right" size={22} color="#CCC" />
        </Pressable>
      ))}
    </View>
  );
};

const styles = StyleSheet.create({
  container: { marginHorizontal: 16, marginTop: 20 },
  sectionTitle: { fontSize: 16, fontWeight: '700', color: '#1A1A1A', marginBottom: 10 },
  card: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#FFF',
    borderRadius: 14,
    padding: 14,
    marginBottom: 8,
    gap: 12,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 1 },
    shadowOpacity: 0.04,
    shadowRadius: 4,
    elevation: 2,
  },
  cardPressed: { opacity: 0.7 },
  iconWrap: { width: 44, height: 44, borderRadius: 12, justifyContent: 'center', alignItems: 'center' },
  cardInfo: { flex: 1 },
  cardTitle: { fontSize: 14, fontWeight: '600', color: '#1A1A1A' },
  cardSubtitle: { fontSize: 12, color: '#888', marginTop: 2 },
});

export default TradeActions;
