// Bottom sheet on RN Modal + reanimated slide/fade (no gesture-handler root needed).
// Dismiss via scrim tap or the close button. Animates in AND out (mounted-state).
//
// Screen-fit layout (opt-in): pass `scrollable` to wrap children in a vertical
// ScrollView that shrinks inside the panel's max height, and/or `footer` to pin
// CTAs below the body (rendered via BottomActionBar, outside any scrolling, with
// the safe-area bottom inset) so the primary action can never be pushed
// off-screen on short devices. Children-only usage renders exactly as before.
import React, { useEffect, useState } from 'react';
import { Modal, Pressable, ScrollView, View } from 'react-native';
import Animated, {
  runOnJS,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withTiming,
} from 'react-native-reanimated';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { useKeyboardHeight } from '@/hooks/useKeyboardHeight';
import { Text } from './Text';
import { IconButton } from './IconButton';
import { BottomActionBar } from './BottomActionBar';

export interface SheetProps {
  visible: boolean;
  onClose: () => void;
  title?: string;
  children: React.ReactNode;
  /** Cap panel height as a fraction of the screen (default 0.85). */
  maxHeightRatio?: number;
  /** Wrap children in a vertical ScrollView so long content scrolls inside the
   *  panel instead of overflowing it (pair with `footer` for a screen-fit sheet).
   *  Leave off when the children manage their own scrolling (e.g. a FlatList). */
  scrollable?: boolean;
  /** Pinned CTA slot rendered below the body, outside any scrolling — always
   *  visible. Owns the safe-area bottom inset when present. */
  footer?: React.ReactNode;
}

export const Sheet: React.FC<SheetProps> = ({
  visible,
  onClose,
  title,
  children,
  maxHeightRatio = 0.85,
  scrollable = false,
  footer,
}) => {
  const { colors, radius, spacing, motion } = useTheme();
  const insets = useSafeAreaInsets();
  const reduced = useReducedMotion();
  // Lift the panel above the keyboard. The Modal is bottom-anchored and RN
  // doesn't resize a Modal window for the keypad, so a sheet with a search input
  // (bank/provider/token pickers) would otherwise have its list buried under the
  // keyboard.
  const kbHeight = useKeyboardHeight();
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
              // With a pinned footer the BottomActionBar owns the bottom inset.
              paddingBottom: footer ? 0 : insets.bottom + spacing.md,
              // Sit above the keyboard when one is open (0 otherwise = no change).
              marginBottom: kbHeight,
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
          {scrollable ? (
            // flexShrink lets the body give way to the header/footer inside the
            // maxHeight cap, becoming scrollable instead of overflowing.
            <ScrollView style={{ flexShrink: 1 }} keyboardShouldPersistTaps="handled" nestedScrollEnabled>
              {children}
            </ScrollView>
          ) : (
            children
          )}
          {footer ? <BottomActionBar>{footer}</BottomActionBar> : null}
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
