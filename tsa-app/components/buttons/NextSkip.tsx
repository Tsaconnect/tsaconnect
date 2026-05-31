// components/buttons/NextSkip.tsx
//
// Onboarding Next / Skip controls. Migrated onto the TSA Signature design system:
// theme tokens (colors / spacing / radius) via useTheme and the @/components/ui
// primitives (Text, Button, PressableScale) instead of the legacy constants/theme
// (COLORS/SIZES) and raw TouchableOpacity/StyleSheet. Behavior is unchanged — same
// props (handleNext/handleSkip, plus index on SkipNextButton), same render logic
// (Skip hidden when index === 3), same Progress.* indicators, and the same named
// + default exports so every onboarding consumer keeps working.
import React from 'react';
import { useWindowDimensions, View } from 'react-native';
import {
  ProgressFour,
  ProgressOne,
  ProgressThree,
  ProgressTwo,
} from './Progress';
import { useTheme } from '@/theme';
import { Button, PressableScale, Text } from '@/components/ui';

interface ButtonProps {
  handleNext: () => void;
  handleSkip: () => void;
}

interface SkipNextButtonProps extends ButtonProps {
  index: number;
}

export const NextBackOne: React.FC<ButtonProps> = ({
  handleNext,
  handleSkip,
}) => {
  const { spacing } = useTheme();
  return (
    <View style={{ alignItems: 'center' }}>
      <View style={{ alignItems: 'center', justifyContent: 'center', marginVertical: spacing.sm }}>
        <Button title="Next" onPress={handleNext} fullWidth={false} />
      </View>
      <PressableScale onPress={handleSkip} style={{ paddingVertical: spacing.sm }}>
        <Text variant="bodyMed" weight="500" color="accent">
          Skip
        </Text>
      </PressableScale>
    </View>
  );
};

const NextBackRow: React.FC<ButtonProps & { children: React.ReactNode }> = ({
  handleNext,
  handleSkip,
  children,
}) => {
  const { spacing } = useTheme();
  return (
    <View
      style={{
        marginTop: spacing['4xl'] + spacing['3xl'],
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
      }}
    >
      <PressableScale onPress={handleSkip} style={{ paddingLeft: spacing['3xl'] + spacing.sm }}>
        <Text variant="bodyMed" weight="500" color="accent">
          Skip
        </Text>
      </PressableScale>
      {children}
      <PressableScale onPress={handleNext} style={{ paddingRight: spacing['3xl'] + spacing.sm }}>
        <Text variant="bodyMed" weight="500" color="accent">
          Next
        </Text>
      </PressableScale>
    </View>
  );
};

export const NextBackTwo: React.FC<ButtonProps> = ({ handleNext, handleSkip }) => (
  <NextBackRow handleNext={handleNext} handleSkip={handleSkip}>
    <ProgressTwo />
  </NextBackRow>
);

export const NextBackThree: React.FC<ButtonProps> = ({ handleNext, handleSkip }) => (
  <NextBackRow handleNext={handleNext} handleSkip={handleSkip}>
    <ProgressThree />
  </NextBackRow>
);

export const NextBackFour: React.FC<ButtonProps> = ({ handleNext, handleSkip }) => (
  <NextBackRow handleNext={handleNext} handleSkip={handleSkip}>
    <ProgressFour />
  </NextBackRow>
);

export const SkipNextButton: React.FC<SkipNextButtonProps> = ({
  handleNext,
  handleSkip,
  index,
}) => {
  const { spacing } = useTheme();
  const { width } = useWindowDimensions();
  return (
    <View style={{ alignItems: 'center', justifyContent: 'center', width }}>
      <ProgressOne index={index} />
      <Button
        title="Next"
        onPress={handleNext}
        fullWidth={false}
        style={{ width: '90%', marginVertical: spacing.lg }}
      />
      {index !== 3 && (
        <PressableScale onPress={handleSkip} style={{ paddingVertical: spacing.sm }}>
          <Text variant="bodyMed" weight="500" color="accent" align="center">
            Skip
          </Text>
        </PressableScale>
      )}
    </View>
  );
};

export default {
  NextBackOne,
  NextBackTwo,
  NextBackThree,
  NextBackFour,
  SkipNextButton,
};
