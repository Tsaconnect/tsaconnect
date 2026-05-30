// Bottom sheet on RN Modal + reanimated slide/fade (no gesture-handler root needed).
// Dismiss via scrim tap or the close button. Animates in AND out (mounted-state).
import React, { useEffect, useState } from 'react';
import { Modal, Pressable, View } from 'react-native';
import Animated, {
  runOnJS,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withTiming,
} from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { IconButton } from './IconButton';

export interface SheetProps {
  visible: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  /** Cap panel height as a fraction of the screen (default 0.85). */
  maxHeightRatio?: number;
}

export const Sheet: React.FC<SheetProps> = ({ visible, onClose, title, children, maxHeightRatio = 0.85 }) => {
  const { colors, radius, spacing, motion } = useTheme();
  const insets = useSafeAreaInsets();
  const reduced = useReducedMotion();
  const [mounted, setMounted] = useState(visible);
  const progress = useSharedValue(0);

  useEffect(() => {
    if (visible) {
      setMounted(true);
      progress.value = reduced ? 1 : withTiming(1, { duration: motion.timing.base });
    } else if (mounted) {
      if (reduced) {
        setMounted(false);
      } else {
        progress.value = withTiming(0, { duration: motion.timing.fast }, (finished) => {
          if (finished) runOnJS(setMounted)(false);
        });
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [visible, reduced]);

  const scrimStyle = useAnimatedStyle(() => ({ opacity: progress.value }));
  const panelStyle = useAnimatedStyle(() => ({
    opacity: progress.value,
    transform: [{ translateY: (1 - progress.value) * 48 }],
  }));

  if (!mounted) return null;

  return (
    <Modal transparent visible={mounted} animationType="none" onRequestClose={onClose} statusBarTranslucent>
      <View style={{ flex: 1, justifyContent: 'flex-end' }}>
        <Animated.View style={[{ ...StyleSheetAbsoluteFill, backgroundColor: colors.overlay }, scrimStyle]}>
          <Pressable accessibilityLabel="Dismiss" style={{ flex: 1 }} onPress={onClose} />
        </Animated.View>
        <Animated.View
          style={[
            {
              maxHeight: `${maxHeightRatio * 100}%`,
              backgroundColor: colors.surface,
              borderTopLeftRadius: radius.xl,
              borderTopRightRadius: radius.xl,
              paddingBottom: insets.bottom + spacing.md,
            },
            panelStyle,
          ]}
        >
          <View style={{ alignItems: 'center', paddingTop: spacing.sm }}>
            <View style={{ width: 36, height: 4, borderRadius: 2, backgroundColor: colors.border }} />
          </View>
          <View
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              justifyContent: 'space-between',
              paddingHorizontal: spacing.lg,
              paddingTop: spacing.sm,
            }}
          >
            {title ? (
              <Text variant="title3" weight="700">
                {title}
              </Text>
            ) : (
              <View />
            )}
            <IconButton icon="close" accessibilityLabel="Close" onPress={onClose} size={20} />
          </View>
          {children}
        </Animated.View>
      </View>
    </Modal>
  );
};

const StyleSheetAbsoluteFill = {
  position: 'absolute' as const,
  top: 0,
  left: 0,
  right: 0,
  bottom: 0,
};
