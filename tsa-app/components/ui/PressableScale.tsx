// Spring-press wrapper — the signature micro-interaction. Scales to 0.97 on press
// using reanimated v4 (already in the native build). Honors reduced-motion.
import React from 'react';
import { Pressable, PressableProps, ViewStyle, StyleProp } from 'react-native';
import Animated, {
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withSpring,
} from 'react-native-reanimated';
import { useTheme } from '@/theme';

const AnimatedPressable = Animated.createAnimatedComponent(Pressable);

export interface PressableScaleProps extends Omit<PressableProps, 'style'> {
  style?: StyleProp<ViewStyle>;
  /** Pressed scale (default 0.97). */
  activeScale?: number;
  /** Reserved for expo-haptics (no-op shim until that native dep lands). */
  haptic?: boolean;
  children?: React.ReactNode;
}

export const PressableScale: React.FC<PressableScaleProps> = ({
  style,
  activeScale = 0.97,
  disabled,
  children,
  ...rest
}) => {
  const { motion } = useTheme();
  const reduced = useReducedMotion();
  const scale = useSharedValue(1);

  const animatedStyle = useAnimatedStyle(() => ({
    transform: [{ scale: scale.value }],
  }));

  return (
    <AnimatedPressable
      accessibilityRole="button"
      disabled={disabled}
      onPressIn={(e) => {
        if (!reduced && !disabled) scale.value = withSpring(activeScale, motion.spring.press);
        rest.onPressIn?.(e);
      }}
      onPressOut={(e) => {
        if (!reduced) scale.value = withSpring(1, motion.spring.press);
        rest.onPressOut?.(e);
      }}
      style={[animatedStyle, style]}
      {...rest}
    >
      {children}
    </AnimatedPressable>
  );
};
