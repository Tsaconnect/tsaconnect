// Anchored dropdown menu — a compact alternative to the bottom Sheet for quick
// option pickers (currency, amount range, filters). A trigger opens a floating
// card positioned just under it (measured in window coords, so it tracks the
// trigger wherever it sits in a scroll view) over a transparent scrim that
// closes on outside tap. Theme-driven; dark mode relies on surfaceElevated +
// border contrast (shadows read poorly on dark, per Surface).
import React, { useCallback, useRef, useState } from 'react';
import { Modal, Pressable, View, useWindowDimensions } from 'react-native';
import Animated, {
  runOnJS,
  useAnimatedStyle,
  useReducedMotion,
  useSharedValue,
  withTiming,
} from 'react-native-reanimated';
import { useTheme } from '@/theme';
import { useElevationStyle } from './Surface';
import { PressableScale } from './PressableScale';

export interface DropdownProps {
  /** Renders the anchor; Dropdown wraps it in a pressable that toggles the menu. */
  renderTrigger: (state: { open: boolean }) => React.ReactNode;
  /** Menu body. Receives `close` and the height it must cap its content to. */
  children: (api: { close: () => void; maxHeight: number }) => React.ReactNode;
  /** Align the menu's start or end edge to the trigger (default 'start'). */
  align?: 'start' | 'end';
  /** Fixed menu width (default 260). Ignored when matchTriggerWidth. */
  menuWidth?: number;
  /** Stretch the menu to the trigger's measured width. */
  matchTriggerWidth?: boolean;
  /** Hard cap on menu height before it's clamped to available space (default 360). */
  maxHeight?: number;
  disabled?: boolean;
}

const GAP = 6;

export const Dropdown: React.FC<DropdownProps> = ({
  renderTrigger,
  children,
  align = 'start',
  menuWidth = 260,
  matchTriggerWidth = false,
  maxHeight = 360,
  disabled,
}) => {
  const { colors, radius, spacing, motion } = useTheme();
  const elevation = useElevationStyle('lg');
  const reduced = useReducedMotion();
  const triggerRef = useRef<View>(null);
  const { width: screenW, height: screenH } = useWindowDimensions();
  const [open, setOpen] = useState(false);
  const [anchor, setAnchor] = useState<{ x: number; y: number; w: number; h: number } | null>(null);
  const progress = useSharedValue(0);

  const close = useCallback(() => {
    if (reduced) {
      setOpen(false);
      return;
    }
    progress.value = withTiming(0, { duration: motion.timing.fast }, (finished) => {
      if (finished) runOnJS(setOpen)(false);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [reduced]);

  const openMenu = () => {
    if (disabled) return;
    triggerRef.current?.measureInWindow((x, y, w, h) => {
      setAnchor({ x, y, w, h });
      setOpen(true);
      progress.value = reduced ? 1 : withTiming(1, { duration: motion.timing.base });
    });
  };

  const menuStyle = useAnimatedStyle(() => ({
    opacity: progress.value,
    transform: [{ translateY: (1 - progress.value) * -6 }],
  }));

  const pad = spacing.sm;
  const mw = matchTriggerWidth && anchor ? anchor.w : menuWidth;
  let left: number = pad;
  let top = 0;
  let cappedMax: number = maxHeight;
  if (anchor) {
    left = align === 'end' ? anchor.x + anchor.w - mw : anchor.x;
    left = Math.max(pad, Math.min(left, screenW - mw - pad));
    top = anchor.y + anchor.h + GAP;
    cappedMax = Math.max(120, Math.min(maxHeight, screenH - top - pad));
  }

  return (
    <View ref={triggerRef} collapsable={false}>
      <PressableScale onPress={openMenu} disabled={disabled} accessibilityRole="button">
        {renderTrigger({ open })}
      </PressableScale>

      <Modal transparent visible={open} animationType="none" onRequestClose={close} statusBarTranslucent>
        <View style={{ flex: 1 }}>
          {/* Scrim sibling (behind the menu) — closes on outside tap. */}
          <Pressable
            accessibilityLabel="Dismiss"
            onPress={close}
            style={{ position: 'absolute', top: 0, left: 0, right: 0, bottom: 0 }}
          />
          {anchor && (
            <Animated.View
              style={[
                {
                  position: 'absolute',
                  top,
                  left,
                  width: mw,
                  maxHeight: cappedMax,
                  backgroundColor: colors.surfaceElevated,
                  borderRadius: radius.md,
                  borderWidth: 1,
                  borderColor: colors.border,
                  overflow: 'hidden',
                },
                elevation,
                menuStyle,
              ]}
            >
              {children({ close, maxHeight: cappedMax })}
            </Animated.View>
          )}
        </View>
      </Modal>
    </View>
  );
};
