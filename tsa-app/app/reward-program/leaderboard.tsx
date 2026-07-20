// app/reward-program/leaderboard.tsx
//
// TP leaderboard — ranks users by TradePoints. Your own standing is pinned even
// when you're outside the top N (the server returns it separately).
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView, RefreshControl } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Card, EmptyState, ScreenHeader, Spinner, Text } from '@/components/ui';
import {
  getTPLeaderboard,
  type LeaderboardEntry,
  type LeaderboardMe,
} from '@/services/rewardProgramApi';

const fmtTP = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: 2 });

const LeaderboardScreen = () => {
  const { colors, spacing, radius } = useTheme();
  const [entries, setEntries] = useState<LeaderboardEntry[]>([]);
  const [me, setMe] = useState<LeaderboardMe | null>(null);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState('');

  const load = useCallback(async () => {
    const r = await getTPLeaderboard();
    if (r.success && r.data) {
      setEntries(r.data.entries ?? []);
      setMe(r.data.me ?? null);
      setError('');
    } else {
      setError(r.message || 'Could not load the leaderboard');
    }
    setLoading(false);
    setRefreshing(false);
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const medal = (rank: number) => (rank === 1 ? '🥇' : rank === 2 ? '🥈' : rank === 3 ? '🥉' : `${rank}`);

  const row = (e: LeaderboardEntry) => (
    <View
      key={e.userId}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        paddingVertical: spacing.md,
        paddingHorizontal: spacing.md,
        borderRadius: radius.md,
        backgroundColor: e.isMe ? colors.accentMuted : 'transparent',
      }}
    >
      <Text variant="bodyMed" weight="700" style={{ width: 32, textAlign: 'center' }}>
        {medal(e.rank)}
      </Text>
      <View style={{ flex: 1 }}>
        <Text variant="bodyMed" weight={e.isMe ? '700' : '600'}>
          {e.username}
          {e.isMe ? ' (you)' : ''}
        </Text>
        <Text variant="caption" color="textMuted">
          Level {e.level}
        </Text>
      </View>
      <Text variant="bodyMed" weight="700">
        {fmtTP(e.tpBalance)} TP
      </Text>
    </View>
  );

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScreenHeader title="Leaderboard" />
      {loading ? (
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center' }}>
          <Spinner label="Loading leaderboard…" />
        </View>
      ) : error ? (
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState icon="closeCircle" title="Couldn’t load the leaderboard" subtitle={error} />
        </View>
      ) : (
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          refreshControl={
            <RefreshControl
              refreshing={refreshing}
              onRefresh={() => {
                setRefreshing(true);
                load();
              }}
              tintColor={colors.accent}
            />
          }
        >
          {/* Your standing, pinned — visible even when outside the top N. */}
          {me && (
            <Card padding="lg" style={{ marginBottom: spacing.lg }}>
              <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
                YOUR RANK
              </Text>
              <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
                <Text variant="title2" weight="700">
                  #{me.rank}
                </Text>
                <Text variant="bodyMed" weight="700">
                  {fmtTP(me.tpBalance)} TP
                </Text>
              </View>
            </Card>
          )}

          {entries.length === 0 ? (
            <EmptyState icon="clock" title="No ranked users yet" subtitle="Earn TradePoints to climb the board." />
          ) : (
            <Card padding="sm">{entries.map(row)}</Card>
          )}
        </ScrollView>
      )}
    </SafeAreaView>
  );
};

export default LeaderboardScreen;
