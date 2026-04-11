import React, { useState } from 'react';
import { View, Text, TouchableOpacity, StyleSheet, ScrollView } from 'react-native';
import { Ionicons } from '@expo/vector-icons';
import SwapScreen from '@/screens/swap';
import { COLORS } from '@/constants';

const GOLD = '#D4AF37';

type Tab = 'swap' | 'spot' | 'allswap';

const TABS: { key: Tab; label: string; number: string }[] = [
  { key: 'swap', label: 'Swap', number: '1' },
  { key: 'spot', label: 'Spot Trade', number: '2' },
  { key: 'allswap', label: 'All swap', number: '3' },
];

const ComingSoon = ({ title, description }: { title: string; description: string }) => (
  <View style={styles.comingSoonContainer}>
    <View style={styles.comingSoonIcon}>
      <Ionicons name="time-outline" size={48} color={GOLD} />
    </View>
    <Text style={styles.comingSoonTitle}>{title}</Text>
    <Text style={styles.comingSoonDesc}>{description}</Text>
    <View style={styles.comingSoonBadge}>
      <Text style={styles.comingSoonBadgeText}>Coming Soon</Text>
    </View>
  </View>
);

const EasySwap = () => {
  const [activeTab, setActiveTab] = useState<Tab>('swap');

  return (
    <View style={styles.container}>
      {/* Tab Bar */}
      <View style={styles.tabBar}>
        {TABS.map((tab) => {
          const active = activeTab === tab.key;
          return (
            <TouchableOpacity
              key={tab.key}
              style={[styles.tab, active && styles.tabActive]}
              onPress={() => setActiveTab(tab.key)}
              activeOpacity={0.7}
            >
              <View style={[styles.tabNumber, active && styles.tabNumberActive]}>
                <Text style={[styles.tabNumberText, active && styles.tabNumberTextActive]}>
                  {tab.number}
                </Text>
              </View>
              <Text style={[styles.tabLabel, active && styles.tabLabelActive]}>
                {tab.label}
              </Text>
            </TouchableOpacity>
          );
        })}
      </View>

      {/* Tab Content */}
      {activeTab === 'swap' && <SwapScreen />}
      {activeTab === 'spot' && (
        <ComingSoon
          title="Spot Trade"
          description="Trade platform-listed assets and import custom tokens. Stay tuned!"
        />
      )}
      {activeTab === 'allswap' && (
        <ComingSoon
          title="All Swap"
          description="Swap across all available token pairs and liquidity pools. Stay tuned!"
        />
      )}
    </View>
  );
};

export default EasySwap;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#FAFAFA',
  },
  tabBar: {
    flexDirection: 'row',
    paddingHorizontal: 20,
    paddingTop: 12,
    paddingBottom: 8,
    gap: 16,
    backgroundColor: '#FAFAFA',
  },
  tab: {
    flexDirection: 'row',
    alignItems: 'center',
    gap: 6,
    paddingVertical: 8,
    paddingHorizontal: 12,
    borderRadius: 20,
  },
  tabActive: {
    backgroundColor: '#FFF8E1',
  },
  tabNumber: {
    width: 22,
    height: 22,
    borderRadius: 11,
    backgroundColor: '#E0E0E0',
    justifyContent: 'center',
    alignItems: 'center',
  },
  tabNumberActive: {
    backgroundColor: GOLD,
  },
  tabNumberText: {
    fontSize: 11,
    fontWeight: '700',
    color: '#888',
  },
  tabNumberTextActive: {
    color: '#FFF',
  },
  tabLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#888',
  },
  tabLabelActive: {
    color: '#1A1A1A',
    fontWeight: '700',
  },
  comingSoonContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 40,
  },
  comingSoonIcon: {
    width: 96,
    height: 96,
    borderRadius: 48,
    backgroundColor: '#FFF8E1',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 24,
  },
  comingSoonTitle: {
    fontSize: 24,
    fontWeight: '700',
    color: '#1A1A1A',
    marginBottom: 8,
  },
  comingSoonDesc: {
    fontSize: 14,
    color: '#888',
    textAlign: 'center',
    lineHeight: 20,
    marginBottom: 24,
  },
  comingSoonBadge: {
    backgroundColor: '#FFF8E1',
    paddingHorizontal: 20,
    paddingVertical: 10,
    borderRadius: 24,
    borderWidth: 1,
    borderColor: '#FDE68A',
  },
  comingSoonBadgeText: {
    fontSize: 14,
    fontWeight: '700',
    color: GOLD,
  },
});
