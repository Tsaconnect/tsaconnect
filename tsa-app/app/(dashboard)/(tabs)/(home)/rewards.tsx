// app/(dashboard)/(tabs)/(home)/rewards.tsx
//
// Route wrapper for the SP8 TP / Rewards dashboard. Thin shell so the screen
// itself (screens/RewardsScreen.tsx) stays route-agnostic and testable.
import React from 'react';
import RewardsScreen from '@/screens/RewardsScreen';

export default function RewardsRoute() {
  return <RewardsScreen />;
}
