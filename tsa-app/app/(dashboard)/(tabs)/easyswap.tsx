// app/(dashboard)/(tabs)/easyswap.tsx
//
// Easyswap — token swap entry. Hosts three sub-views (Swap / Spot Trade / All
// Swap) behind a tab selector. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives): the legacy numbered-pill tab bar
// is now a SegmentedControl and the "Coming Soon" placeholder is rebuilt on
// EmptyState + Badge. The swap quote/amount/token-selection/submit flows live
// inside SwapScreen / AllSwapScreen and are rendered verbatim — unchanged.
import React, { useState, useEffect } from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import SwapScreen from '@/screens/swap';
import AllSwapScreen from '@/screens/all-swap';
import { useTheme } from '@/theme';
import { Badge, EmptyState, SegmentedControl } from '@/components/ui';

type Tab = 'swap' | 'spot' | 'allswap';

const TABS: { key: Tab; label: string }[] = [
  { key: 'swap', label: 'Swap' },
  { key: 'spot', label: 'Spot Trade' },
  { key: 'allswap', label: 'All Swap' },
];

const ComingSoon = ({ title, description }: { title: string; description: string }) => {
  const { colors, spacing } = useTheme();
  return (
    <View style={{ flex: 1, justifyContent: 'center' }}>
      <EmptyState icon="clock" title={title} subtitle={description} />
      <View style={{ alignItems: 'center', marginTop: spacing.md }}>
        <Badge label="Coming Soon" color={colors.accent} background={colors.accentMuted} />
      </View>
    </View>
  );
};

const EasySwap = () => {
  const { colors, spacing } = useTheme();
  const { tab } = useLocalSearchParams<{ tab?: string }>();
  const [activeTab, setActiveTab] = useState<Tab>('swap');

  useEffect(() => {
    if (tab === 'swap' || tab === 'spot' || tab === 'allswap') {
      setActiveTab(tab);
    }
  }, [tab]);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.sm }}>
        {/* Same TABS registration (keys/labels/order) and the same setActiveTab
            behavior the legacy numbered-pill bar had — only the chrome changed. */}
        <SegmentedControl
          segments={TABS}
          value={activeTab}
          onChange={(k) => setActiveTab(k as Tab)}
        />
      </View>

      {activeTab === 'swap' && <SwapScreen />}
      {activeTab === 'spot' && (
        <ComingSoon title="Spot Trade" description="Trade platform-listed assets. Coming soon!" />
      )}
      {activeTab === 'allswap' && <AllSwapScreen />}
    </SafeAreaView>
  );
};

export default EasySwap;
