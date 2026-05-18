// app/admin/_layout.tsx
//
// Stack layout for admin-only screens. The backend enforces `admin` /
// `super_admin` role via the RequireP2PAdmin middleware; the UI just
// reads the user's role to decide whether to surface the entry points.
import React from 'react';
import { Stack } from 'expo-router';
import { defaultScreenOptions } from '../../constants/navigation';

const AdminLayout = () => (
  <Stack screenOptions={defaultScreenOptions}>
    <Stack.Screen name="p2p-disputes" options={{ title: 'P2P Disputes' }} />
  </Stack>
);

export default AdminLayout;
