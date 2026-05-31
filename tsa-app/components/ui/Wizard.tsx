// Controlled multi-step shell for forms. Renders a "Step N of M" header with the
// step titles (active highlighted), the active step's content, and a docked footer
// (BottomActionBar) with Back (hidden on step 0) + Next/Submit. The CTA gates on
// canAdvance, shows loading when submitting, and switches to submitLabel on the last
// step (calling onSubmit instead of onNext). A subtle slide+fade plays between steps;
// reduced-motion users get an instant swap.
import React, { useEffect, useRef, useState } from 'react';
import { ScrollView, View, ViewStyle } from 'react-native';
import Animated, {
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withTiming,
} from 'react-native-reanimated';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { Button } from './Button';
import { BottomActionBar } from './BottomActionBar';

export interface WizardStep {
  key: string;
  title: string;
}

export interface WizardProps {
  steps: WizardStep[];
  /** Index of the active step (controlled). */
  current: number;
  onBack: () => void;
  onNext: () => void;
  /** Gates the Next / Submit button. */
  canAdvance: boolean;
  /** Called instead of onNext on the last step. */
  onSubmit: () => void;
  submitting?: boolean;
  /** CTA label on the last step (default "Submit"). */
  submitLabel?: string;
  /** The active step's content. */
  children: React.ReactNode;
  /** Wraps step content in a ScrollView (default true). */
  scrollable?: boolean;
  style?: ViewStyle;
  contentStyle?: ViewStyle;
}

export const Wizard: React.FC<WizardProps> = ({
  steps,
  current,
  onBack,
  onNext,
  canAdvance,
  onSubmit,
  submitting,
  submitLabel = 'Submit',
  children,
  scrollable = true,
  style,
  contentStyle,
}) => {
  const { colors, spacing, motion } = useTheme();
  const reduced = useReducedMotion();

  const total = steps.length;
  const index = Math.min(Math.max(current, 0), Math.max(total - 1, 0));
  const isLast = index >= total - 1;
  const isFirst = index <= 0;
  const activeStep = steps[index];

  // Direction of the slide: +1 when advancing, -1 when going back.
  const prevIndex = useRef(index);
  const direction = index >= prevIndex.current ? 1 : -1;

  // Drive a per-step enter animation. We re-key the animated content on `index`
  // so each step mounts fresh and slides/fades in from the travel direction.
  const progress = useSharedValue(reduced ? 1 : 0);

  useEffect(() => {
    prevIndex.current = index;
    if (reduced) {
      progress.value = 1;
    } else {
      progress.value = 0;
      progress.value = withTiming(1, { duration: motion.timing.base });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [index, reduced]);

  const contentAnim = useAnimatedStyle(() => ({
    opacity: progress.value,
    transform: [{ translateX: (1 - progress.value) * 16 * direction }],
  }));

  return (
    <View style={[{ flex: 1, backgroundColor: colors.background }, style]}>
      {/* Step header */}
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, gap: spacing.xs }}>
        <Text variant="overline" color="textMuted">
          {`STEP ${index + 1} OF ${total}`}
        </Text>
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', alignItems: 'center', gap: spacing.xs }}>
          {steps.map((step, i) => {
            const isActive = i === index;
            const isDone = i < index;
            return (
              <View key={step.key} style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Text
                  variant={isActive ? 'bodyMed' : 'bodySm'}
                  weight={isActive ? '700' : '500'}
                  color={isActive ? 'textPrimary' : isDone ? 'textSecondary' : 'textMuted'}
                >
                  {step.title}
                </Text>
                {i < total - 1 && (
                  <Text variant="bodySm" color="textMuted">
                    {'›'}
                  </Text>
                )}
              </View>
            );
          })}
        </View>
        {/* Progress track */}
        <ProgressTrack total={total} index={index} reduced={reduced} duration={motion.timing.base} />
      </View>

      {/* Active step content */}
      {scrollable ? (
        <ScrollView
          contentContainerStyle={[
            { padding: spacing.lg, paddingBottom: spacing['3xl'], flexGrow: 1 },
            contentStyle,
          ]}
          keyboardShouldPersistTaps="handled"
        >
          <Animated.View key={activeStep?.key ?? index} style={[{ flex: 1 }, contentAnim]}>
            {children}
          </Animated.View>
        </ScrollView>
      ) : (
        <Animated.View
          key={activeStep?.key ?? index}
          style={[{ flex: 1, padding: spacing.lg }, contentStyle, contentAnim]}
        >
          {children}
        </Animated.View>
      )}

      {/* Docked footer */}
      <BottomActionBar>
        <View style={{ flexDirection: 'row', gap: spacing.sm }}>
          {!isFirst && (
            <Button
              title="Back"
              variant="secondary"
              leftIcon="chevronLeft"
              fullWidth={false}
              onPress={onBack}
              disabled={submitting}
              style={{ flex: 1 }}
            />
          )}
          <Button
            title={isLast ? submitLabel : 'Next'}
            rightIcon={isLast ? undefined : 'arrowRight'}
            fullWidth={false}
            disabled={!canAdvance}
            loading={submitting}
            onPress={isLast ? onSubmit : onNext}
            style={{ flex: isFirst ? 1 : 2, alignSelf: 'stretch' }}
          />
        </View>
      </BottomActionBar>
    </View>
  );
};

/** Thin animated progress track that fills proportionally to the current step. */
const ProgressTrack: React.FC<{ total: number; index: number; reduced: boolean; duration: number }> = ({
  total,
  index,
  reduced,
  duration,
}) => {
  const { colors, radius } = useTheme();
  const [width, setWidth] = useState(0);
  // Fraction of the bar that should be filled (>= a sliver on the first step).
  const fraction = total > 1 ? (index + 1) / total : 1;
  const fill = useSharedValue(reduced ? fraction : 0);

  useEffect(() => {
    if (reduced) {
      fill.value = fraction;
    } else {
      fill.value = withTiming(fraction, { duration });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fraction, reduced]);

  const fillStyle = useAnimatedStyle(() => ({ width: width * fill.value }));

  return (
    <View
      onLayout={(e) => setWidth(e.nativeEvent.layout.width)}
      style={{ height: 4, borderRadius: radius.pill, backgroundColor: colors.surfaceAlt, overflow: 'hidden' }}
    >
      <Animated.View
        style={[{ height: 4, borderRadius: radius.pill, backgroundColor: colors.accent }, fillStyle]}
      />
    </View>
  );
};
