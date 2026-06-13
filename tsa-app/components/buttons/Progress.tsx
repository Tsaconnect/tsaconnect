// components/buttons/Progress.tsx
//
// Onboarding pager progress indicators (active page marker + inactive Dots).
// Migrated onto the TSA Signature design system: theme tokens (colors / spacing /
// radius) via useTheme instead of the legacy constants/theme COLORS and raw magic
// numbers. The active marker uses colors.accent (the brand gold, formerly
// COLORS.primary). Behavior + public API are unchanged — same named exports
// (ProgressOne/Two/Three/Four), same ProgressOne `index` prop, same dot layout —
// so the onboarding consumer (./NextSkip.tsx) keeps working untouched.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import Dots from './Dots';

interface ProgressOneProps {
  index: number;
}

export const ProgressOne: React.FC<ProgressOneProps> = ({ index }) => {
  const { colors, spacing, radius } = useTheme();
  const activeDot = {
    backgroundColor: colors.accent,
    width: spacing.md - 2,
    height: spacing.md - 2,
    borderRadius: radius.pill,
    margin: spacing.xs + 1,
  };
  return (
    <View style={{ flexDirection: 'row', justifyContent: 'center' }}>
      {index === 1 && (
        <>
          <View style={activeDot} />
          <Dots />
          <Dots />
        </>
      )}
      {index === 2 && (
        <>
          <Dots />
          <View style={activeDot} />
          <Dots />
        </>
      )}
      {index === 3 && (
        <>
          <Dots />
          <Dots />
          <View style={activeDot} />
        </>
      )}
    </View>
  );
};

// Larger active marker used by the row-style Next/Back indicators.
const LargeActiveDot: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  return (
    <View
      style={{
        backgroundColor: colors.accent,
        width: spacing.md + 2,
        height: spacing.md + 2,
        borderRadius: radius.pill,
        margin: spacing.xs + 1,
        marginTop: spacing.xs - 1,
      }}
    />
  );
};

const ProgressContainer: React.FC<{ children: React.ReactNode }> = ({ children }) => (
  <View style={{ flexDirection: 'row', justifyContent: 'center', flex: 1 }}>{children}</View>
);

export const ProgressTwo: React.FC = () => (
  <ProgressContainer>
    <Dots />
    <LargeActiveDot />
    <Dots />
  </ProgressContainer>
);

export const ProgressThree: React.FC = () => (
  <ProgressContainer>
    <Dots />
    <Dots />
    <LargeActiveDot />
    <Dots />
  </ProgressContainer>
);

export const ProgressFour: React.FC = () => (
  <ProgressContainer>
    <Dots />
    <Dots />
    <Dots />
    <LargeActiveDot />
  </ProgressContainer>
);
