// Vertical progress stepper: one row per step, a marker per state, and a rail
// joining them. The single rendering behind every "where is my money" list —
// ramp orders, conversion hops, TSA Pay attempts, merchant order timelines.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Icon } from './Icon';
import { Spinner } from './Spinner';
import type { Step, StepState } from '@/utils/progressSteps';
import { Text } from './Text';

export type { Step, StepState };

export interface StepperProps {
  steps: Step[];
}

const MARKER = 22;

/** The marker's meaning in words — a screen reader cannot see a check or a spinner. */
const STATE_WORDS: Record<StepState, string> = {
  done: 'done',
  active: 'in progress',
  pending: 'not started',
  failed: 'failed',
};

export const Stepper: React.FC<StepperProps> = ({ steps }) => {
  const { colors, spacing } = useTheme();

  const marker = (state: StepState) => {
    switch (state) {
      case 'done':
        return (
          <View style={[markerBase, { backgroundColor: colors.success }]}>
            <Icon name="check" size={13} color={colors.onAccent} />
          </View>
        );
      case 'failed':
        return (
          <View style={[markerBase, { backgroundColor: colors.danger }]}>
            <Icon name="close" size={13} color={colors.onAccent} />
          </View>
        );
      case 'active':
        return (
          <View style={[markerBase, { borderWidth: 2, borderColor: colors.accent }]}>
            <Spinner size="small" color={colors.accent} />
          </View>
        );
      case 'pending':
        return <View style={[markerBase, { borderWidth: 2, borderColor: colors.border }]} />;
    }
  };

  return (
    <View>
      {steps.map((step, i) => {
        const last = i === steps.length - 1;
        const emphasised = step.current || step.state === 'active' || step.state === 'failed';
        return (
          <View
            key={step.key}
            accessible
            accessibilityLabel={[step.label, STATE_WORDS[step.state], step.detail].filter(Boolean).join(', ')}
            style={{ flexDirection: 'row' }}
          >
            <View style={{ width: MARKER, alignItems: 'center' }}>
              {marker(step.state)}
              {!last && (
                <View
                  style={{
                    width: 2,
                    flex: 1,
                    minHeight: spacing.md,
                    marginVertical: 2,
                    backgroundColor: step.state === 'done' ? colors.success : colors.border,
                  }}
                />
              )}
            </View>
            <View style={{ flex: 1, marginLeft: spacing.md, paddingBottom: last ? 0 : spacing.md, minHeight: MARKER }}>
              <Text
                variant="bodySm"
                weight={emphasised ? '700' : '500'}
                color={step.state === 'failed' ? 'danger' : step.state === 'pending' ? 'textMuted' : 'textPrimary'}
                style={{ lineHeight: MARKER }}
              >
                {step.label}
              </Text>
              {step.detail ? (
                <Text variant="caption" color="textMuted">
                  {step.detail}
                </Text>
              ) : null}
            </View>
          </View>
        );
      })}
    </View>
  );
};

const markerBase = {
  width: MARKER,
  height: MARKER,
  borderRadius: MARKER / 2,
  alignItems: 'center' as const,
  justifyContent: 'center' as const,
};
