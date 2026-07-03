// app/admin/spot-listings/[id].tsx — edit an existing Spot listing.
// Loads the row from the admin list (there's no single-get endpoint) and hands
// it to the shared form.
import React, { useEffect, useState } from 'react';
import { View } from 'react-native';
import { useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import { EmptyState, Spinner } from '@/components/ui';
import SpotListingForm from '@/components/spot/SpotListingForm';
import { listSpotListingsAdmin, type SpotListing } from '@/services/spotApi';

export default function EditSpotListingScreen() {
  const { id } = useLocalSearchParams<{ id: string }>();
  const { colors } = useTheme();
  const [listing, setListing] = useState<SpotListing | null>(null);
  const [state, setState] = useState<'loading' | 'ready' | 'notfound'>('loading');

  useEffect(() => {
    (async () => {
      const r = await listSpotListingsAdmin();
      const found = r.success && r.data ? r.data.listings.find((l) => l.id === id) : undefined;
      if (found) {
        setListing(found);
        setState('ready');
      } else {
        setState('notfound');
      }
    })();
  }, [id]);

  if (state === 'loading') {
    return (
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        <Spinner label="Loading…" />
      </View>
    );
  }

  if (state === 'notfound' || !listing) {
    return (
      <View style={{ flex: 1, justifyContent: 'center', backgroundColor: colors.background }}>
        <EmptyState icon="closeCircle" title="Listing not found" subtitle="It may have been deleted." />
      </View>
    );
  }

  return <SpotListingForm mode="edit" initial={listing} />;
}
