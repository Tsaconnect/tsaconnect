// Stack layout for the legal documents (Privacy Policy, Terms of Service).
// Reached from the signup form's consent links, i.e. from a completely
// different group — without the shared headerLeft these are a dead end in the
// middle of onboarding. Titles come from each screen's own <Stack.Screen> so
// the document title and the header can never drift apart.
import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';

const LegalLayout = () => {
  const screenOptions = useDefaultScreenOptions();
  return <Stack screenOptions={screenOptions} />;
};

export default LegalLayout;
