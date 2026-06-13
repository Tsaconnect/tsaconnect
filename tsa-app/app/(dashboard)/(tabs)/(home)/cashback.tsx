// app/(dashboard)/(tabs)/(home)/cashback.tsx
//
// Route wrapper for the Cashback summary screen. Thin shell so the screen
// itself (screens/CashbackScreen.tsx) stays route-agnostic and testable.
import React from 'react';
import CashbackScreen from '@/screens/CashbackScreen';

export default function CashbackRoute() {
  return <CashbackScreen />;
}
