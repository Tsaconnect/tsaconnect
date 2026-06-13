// screens/activity.tsx
//
// Unified activity feed screen (wallet transaction history + swaps + TP / cashback
// / orders / services). Hosts the shared <ActivityFeed> with filters enabled.
// Restyled onto the TSA Signature design system: the legacy hardcoded #FAFAFA
// background + magic padding is replaced with a themed SafeAreaView. The feed's
// data fetching, pagination, filtering, and item navigation all live in
// ActivityFeed and are unchanged.
import React from 'react';
import { View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import ActivityFeed from '@/components/activity/ActivityFeed';
import { type ActivityType } from '@/services/activityService';
import { useTheme } from '@/theme';

interface Props {
  /** Pre-selected types when opened from a specific feature. Empty = All. */
  initialTypes?: ActivityType[];
}

export default function ActivityScreen({ initialTypes = [] }: Props) {
  const { colors, spacing } = useTheme();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, paddingTop: spacing.sm }}>
        <ActivityFeed types={initialTypes} showFilters />
      </View>
    </SafeAreaView>
  );
}
