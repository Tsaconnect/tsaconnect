// Stack for the Reward-programme forms. Headers are hidden: each screen
// renders the shared ScreenHeader so back is consistent with the rest of the app.
import React from 'react';
import { Stack } from 'expo-router';

const RewardProgramLayout = () => (
  <Stack screenOptions={{ headerShown: false }}>
    <Stack.Screen name="signup" />
    <Stack.Screen name="onboarding" />
    <Stack.Screen name="leaderboard" />
  </Stack>
);

export default RewardProgramLayout;
