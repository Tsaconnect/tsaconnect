// app/admin/_layout.tsx
//
// Stack layout for the in-app admin console. Screens registered here share the
// app-wide header chrome (defaultScreenOptions). The P2P dispute console lives
// under this group; per-screen role gating (admin / super_admin) is enforced
// inside each screen, not here, so a clear "not authorized" UI can render.
import React from 'react';
import { Stack } from 'expo-router';
import { defaultScreenOptions } from '../../constants/navigation';

const AdminLayout = () => {
  return (
    <Stack screenOptions={defaultScreenOptions}>
      <Stack.Screen name="p2p-disputes" options={{ title: 'P2P Disputes' }} />
      <Stack.Screen
        name="p2p-disputes/[tradeId]"
        options={{ title: 'Resolve Dispute' }}
      />
      <Stack.Screen name="pm-queue" options={{ title: 'Manual Release Queue' }} />
      <Stack.Screen
        name="pm-queue/[tradeId]"
        options={{ title: 'Review PM Trade' }}
      />
      <Stack.Screen name="pm-applications" options={{ title: 'PM Applications' }} />
      <Stack.Screen name="pm-status" options={{ title: 'PM Status' }} />
      <Stack.Screen name="spot-listings" options={{ title: 'Spot Listings' }} />
      <Stack.Screen name="spot-listings/add" options={{ title: 'Add Listing' }} />
      <Stack.Screen name="spot-listings/[id]" options={{ title: 'Edit Listing' }} />
    </Stack>
  );
};

export default AdminLayout;
