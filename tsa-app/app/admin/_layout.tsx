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
    </Stack>
  );
};

export default AdminLayout;
