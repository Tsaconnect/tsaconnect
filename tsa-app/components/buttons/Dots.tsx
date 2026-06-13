// components/buttons/Dots.tsx
//
// Onboarding pager dots indicator — the inactive dot rendered between/around
// the active page marker (see ./Progress.tsx). Migrated onto the TSA Signature
// design system: theme tokens (colors / spacing / radius) via useTheme instead
// of the legacy constants/theme COLORS. No props/exports changed — consumers
// (Progress.tsx) still render <Dots /> with no arguments.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';

const Dots = () => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        backgroundColor: colors.textSecondary,
        width: spacing.md - 2,
        height: spacing.md - 2,
        borderRadius: radius.pill,
        margin: spacing.xs + 1,
      }}
    />
  );
};

export default Dots;
