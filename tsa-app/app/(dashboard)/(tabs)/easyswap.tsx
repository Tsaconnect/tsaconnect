// app/(dashboard)/(tabs)/easyswap.tsx
//
// Easyswap — token swap entry. Hosts three sub-views (OTC / Spot Trade / All
// Swap) behind a tab selector. The OTC pane is a listing CATALOGUE; picking a
// row's Buy/Sell pushes the trade form at /otc-trade. Restyled onto the TSA Signature design system
// (theme tokens + @/components/ui primitives): the legacy numbered-pill tab bar
// is now a SegmentedControl and the "Coming Soon" placeholder is rebuilt on
// EmptyState + Badge. The swap quote/amount/token-selection/submit flows live
// inside SwapScreen / AllSwapScreen and are rendered verbatim — unchanged.
import React, { useState, useEffect, useMemo } from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useLocalSearchParams } from 'expo-router';
import OtcListingList from '@/components/otc/OtcListingList';
import AllSwapScreen from '@/screens/all-swap';
import SpotList from '@/components/spot/SpotList';
import { singleParam } from '@/utils/spotChains';
import { useTheme } from '@/theme';
import { SegmentedControl } from '@/components/ui';

type Tab = 'swap' | 'spot' | 'allswap';

// Key stays 'swap' (deep links use ?tab=swap); only the label is "OTC".
const TABS: { key: Tab; label: string }[] = [
  { key: 'swap', label: 'OTC' },
  { key: 'spot', label: 'Spot Trade' },
  { key: 'allswap', label: 'All Swap' },
];

const EasySwap = () => {
  const { colors, spacing } = useTheme();
  // Not typed as `{ tab?: string }` for the new params: expo-router really can
  // hand back `string[]` for a repeated query param, and it rebuilds that array
  // every render. singleParam collapses both hazards at the edge, before the
  // preselection memo can depend on the value — see the note on it.
  const params = useLocalSearchParams();
  const tab = singleParam(params.tab);
  const fromSymbol = singleParam(params.fromSymbol);
  const fromChain = singleParam(params.fromChain);
  const toSymbol = singleParam(params.toSymbol);
  const toChain = singleParam(params.toChain);
  const [activeTab, setActiveTab] = useState<Tab>('swap');

  // A fresh object each render would restart All Swap's preselection — and so
  // its token lookups — on every keystroke the swap form takes.
  const preselect = useMemo(
    () => (fromSymbol || toSymbol
      ? { fromSymbol, fromChain, toSymbol, toChain }
      : undefined),
    [fromSymbol, fromChain, toSymbol, toChain],
  );

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

      {activeTab === 'swap' && <OtcListingList />}
      {activeTab === 'spot' && <SpotList />}
      {activeTab === 'allswap' && <AllSwapScreen preselect={preselect} />}
    </SafeAreaView>
  );
};

export default EasySwap;
