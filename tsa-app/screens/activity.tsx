import React from 'react';
import { View, StyleSheet } from 'react-native';
import ActivityFeed from '@/components/activity/ActivityFeed';
import { type ActivityType } from '@/services/activityService';

interface Props {
  /** Pre-selected types when opened from a specific feature. Empty = All. */
  initialTypes?: ActivityType[];
}

export default function ActivityScreen({ initialTypes = [] }: Props) {
  return (
    <View style={s.container}>
      <ActivityFeed types={initialTypes} showFilters />
    </View>
  );
}

const s = StyleSheet.create({
  container: { flex: 1, backgroundColor: '#FAFAFA', paddingTop: 8 },
});
