// Shimmer placeholder for loading states (reduced-motion aware). SkeletonGroup
// provides ready-made shapes for the common P2P lists.
import React, { useEffect } from 'react';
import { DimensionValue, View, ViewStyle } from 'react-native';
import Animated, {
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withRepeat,
  withTiming,
} from 'react-native-reanimated';
import { RadiusKey, useTheme } from '@/theme';

export interface SkeletonProps {
  width?: DimensionValue;
  height?: number;
  radius?: RadiusKey;
  style?: ViewStyle;
}

export const Skeleton: React.FC<SkeletonProps> = ({ width = '100%', height = 16, radius = 'sm', style }) => {
  const { colors, radius: r } = useTheme();
  const reduced = useReducedMotion();
  const o = useSharedValue(reduced ? 0.6 : 0.4);

  useEffect(() => {
    if (reduced) return;
    o.value = withRepeat(withTiming(0.85, { duration: 800 }), -1, true);
  }, [reduced, o]);

  const animatedStyle = useAnimatedStyle(() => ({ opacity: o.value }));

  return (
    <Animated.View
      style={[
        { width, height, borderRadius: r[radius], backgroundColor: colors.surfaceAlt },
        animatedStyle,
        style,
      ]}
    />
  );
};

/** Common skeleton shapes. `count` repeats the shape. */
export const SkeletonGroup: React.FC<{ shape?: 'adCard' | 'tradeRow' | 'line'; count?: number }> = ({
  shape = 'adCard',
  count = 3,
}) => {
  const { spacing } = useTheme();
  const items = Array.from({ length: count });
  if (shape === 'line') {
    return (
      <View style={{ gap: spacing.sm }}>
        {items.map((_, i) => (
          <Skeleton key={i} height={14} width={`${90 - i * 12}%`} />
        ))}
      </View>
    );
  }
  return (
    <View style={{ gap: spacing.md }}>
      {items.map((_, i) => (
        <View
          key={i}
          style={{ flexDirection: 'row', gap: spacing.md, padding: spacing.lg }}
        >
          <Skeleton width={40} height={40} radius="pill" />
          <View style={{ flex: 1, gap: spacing.sm }}>
            <Skeleton height={16} width="55%" />
            <Skeleton height={20} width="40%" />
            {shape === 'adCard' && <Skeleton height={12} width="70%" />}
          </View>
        </View>
      ))}
    </View>
  );
};
