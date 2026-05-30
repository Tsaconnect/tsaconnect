// Animated segmented control (Buy/Sell tabs, rate-type toggle, generic segments).
import React, { useEffect, useState } from 'react';
import { LayoutChangeEvent, Pressable, View, ViewStyle } from 'react-native';
import Animated, { useAnimatedStyle, useSharedValue, withTiming } from 'react-native-reanimated';
import { useTheme } from '@/theme';
import { Text } from './Text';

export interface Segment<T extends string = string> {
  key: T;
  label: string;
}

export interface SegmentedControlProps<T extends string = string> {
  segments: Segment<T>[];
  value: T;
  onChange: (key: T) => void;
  style?: ViewStyle;
}

export function SegmentedControl<T extends string = string>({
  segments,
  value,
  onChange,
  style,
}: SegmentedControlProps<T>) {
  const { colors, radius, spacing, motion } = useTheme();
  const [width, setWidth] = useState(0);
  const index = Math.max(0, segments.findIndex((s) => s.key === value));
  const segW = width > 0 ? (width - 4) / segments.length : 0;
  const tx = useSharedValue(0);

  useEffect(() => {
    tx.value = withTiming(index * segW, { duration: motion.timing.base });
  }, [index, segW, tx, motion.timing.base]);

  const thumbStyle = useAnimatedStyle(() => ({ transform: [{ translateX: tx.value }] }));

  const onLayout = (e: LayoutChangeEvent) => setWidth(e.nativeEvent.layout.width);

  return (
    <View
      onLayout={onLayout}
      style={[
        {
          flexDirection: 'row',
          backgroundColor: colors.surfaceAlt,
          borderRadius: radius.md,
          padding: 2,
        },
        style,
      ]}
    >
      {segW > 0 && (
        <Animated.View
          style={[
            {
              position: 'absolute',
              top: 2,
              left: 2,
              bottom: 2,
              width: segW,
              borderRadius: radius.md - 2,
              backgroundColor: colors.surface,
            },
            thumbStyle,
          ]}
        />
      )}
      {segments.map((s) => {
        const active = s.key === value;
        return (
          <Pressable
            key={s.key}
            onPress={() => onChange(s.key)}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            style={{ flex: 1, alignItems: 'center', justifyContent: 'center', paddingVertical: spacing.sm, minHeight: 40 }}
          >
            <Text variant="bodySm" weight={active ? '700' : '500'} color={active ? 'textPrimary' : 'textSecondary'}>
              {s.label}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
}
