// app/admin/spot-listings.tsx
//
// Admin screen: manage the Spot Trade listings shown on the Easyswap → Spot tab.
// List (incl. disabled), enable/disable inline, edit, delete, and add new pairs.
// Buy/Sell on the user side reuse existing flows (see components/spot/SpotList).
//
// Pattern mirrors app/admin/pm-applications.tsx (role gate via useUserRole,
// load-on-focus, pull-to-refresh, loading/empty/error states, toast feedback).
import { useFocusEffect } from '@react-navigation/native';
import React, { useCallback, useState } from 'react';
import { Alert, RefreshControl, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Badge,
  Button,
  Card,
  EmptyState,
  ErrorState,
  Icon,
  PressableScale,
  SkeletonGroup,
  Text,
  Toggle,
  useToast,
} from '@/components/ui';
import {
  listSpotListingsAdmin,
  updateSpotListing,
  deleteSpotListing,
  type SpotListing,
} from '@/services/spotApi';
import { useUserRole } from '@/hooks/useUserRole';

const ADMIN_ROLES = new Set(['admin', 'super_admin', 'superadmin']);

const SpotListingsAdminScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const toast = useToast();
  const { role, loading: roleLoading } = useUserRole();

  const [listings, setListings] = useState<SpotListing[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);
  const [error, setError] = useState(false);
  const [busyId, setBusyId] = useState<string | null>(null);

  const isAdmin = !!role && ADMIN_ROLES.has(role.toLowerCase());

  const load = useCallback(async () => {
    if (!isAdmin) {
      setLoading(false);
      return;
    }
    setError(false);
    const res = await listSpotListingsAdmin();
    if (res.success && res.data) {
      setListings(res.data.listings ?? []);
    } else {
      setError(true);
    }
    setLoading(false);
    setRefreshing(false);
  }, [isAdmin]);

  useFocusEffect(
    useCallback(() => {
      if (!roleLoading) {
        setLoading(true);
        load();
      }
    }, [roleLoading, load]),
  );

  const toggleEnabled = useCallback(
    async (l: SpotListing) => {
      setBusyId(l.id);
      const res = await updateSpotListing(l.id, { enabled: !l.enabled });
      setBusyId(null);
      if (res.success) {
        setListings((prev) => prev.map((x) => (x.id === l.id ? { ...x, enabled: !l.enabled } : x)));
      } else {
        toast.error(res.message || 'Could not update listing');
      }
    },
    [toast],
  );

  const handleDelete = useCallback(
    (l: SpotListing) => {
      Alert.alert('Delete listing?', `Remove ${l.baseSymbol}/${l.quoteSymbol} (${l.chain}) from the Spot tab?`, [
        { text: 'Cancel', style: 'cancel' },
        {
          text: 'Delete',
          style: 'destructive',
          onPress: async () => {
            setBusyId(l.id);
            const res = await deleteSpotListing(l.id);
            setBusyId(null);
            if (res.success) {
              setListings((prev) => prev.filter((x) => x.id !== l.id));
              toast.success('Listing deleted');
            } else {
              toast.error(res.message || 'Could not delete listing');
            }
          },
        },
      ]);
    },
    [toast],
  );

  if (!roleLoading && !isAdmin) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center' }}>
          <EmptyState
            icon="lock"
            title="Admins only"
            subtitle="Spot listing management is restricted to admin and super-admin accounts."
            actionLabel="Go back"
            onAction={() => router.back()}
          />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        refreshControl={
          <RefreshControl
            refreshing={refreshing}
            tintColor={colors.accent}
            onRefresh={() => {
              setRefreshing(true);
              load();
            }}
          />
        }
      >
        <Text variant="title1" weight="700">
          Spot Listings
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
          Curate the pairs shown on the Spot Trade tab. Buy/Sell reuse existing swap flows.
        </Text>

        <Button
          title="Add listing"
          leftIcon="plus"
          onPress={() => router.push('/admin/spot-listings/add' as any)}
          style={{ marginBottom: spacing.lg }}
        />

        {loading || roleLoading ? (
          <SkeletonGroup shape="tradeRow" count={4} />
        ) : error ? (
          <ErrorState
            subtitle="Couldn't load spot listings. Check your connection."
            onRetry={() => {
              setLoading(true);
              load();
            }}
          />
        ) : listings.length === 0 ? (
          <EmptyState
            icon="clock"
            title="No listings yet"
            subtitle="Tap “Add listing” to publish your first spot pair."
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {listings.map((l) => (
              <Card key={l.id} padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                  <PressableScale
                    style={{ flex: 1 }}
                    onPress={() =>
                      router.push({ pathname: '/admin/spot-listings/[id]', params: { id: l.id } } as any)
                    }
                  >
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                      <Text variant="bodyMed" weight="700">
                        {l.baseSymbol}/{l.quoteSymbol}
                      </Text>
                      <Badge label={l.enabled ? 'Live' : 'Off'} size="sm" />
                    </View>
                    <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
                      {l.chain} · route: {l.route} · order {l.displayOrder}
                    </Text>
                  </PressableScale>

                  <Toggle
                    value={l.enabled}
                    onValueChange={() => toggleEnabled(l)}
                    disabled={busyId === l.id}
                    accessibilityLabel={`Toggle ${l.baseSymbol}`}
                  />
                  <PressableScale
                    onPress={() => handleDelete(l)}
                    disabled={busyId === l.id}
                    accessibilityRole="button"
                    accessibilityLabel="Delete listing"
                    hitSlop={8}
                  >
                    <Icon name="trash" size={20} color={colors.danger} />
                  </PressableScale>
                </View>
              </Card>
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

export default SpotListingsAdminScreen;
