// Inline error / warning / info shown next to the field or section it is about.
// Unlike a toast it never times out: it stays until the user acts (dismiss,
// retry, edit) or the owning screen clears the state. Text wraps in full —
// no numberOfLines — so long API messages stay readable. The box is AccentCard
// (the app's status-rail card) on its tone's tint; this adds the tone mapping,
// the alert semantics and the dismiss / recovery affordances.
import React from 'react';
import { Pressable, StyleProp, View, ViewStyle } from 'react-native';
import Animated, { FadeIn } from 'react-native-reanimated';
import { ColorTokens, useTheme } from '@/theme';
import { AccentCard } from './Card';
import { Text } from './Text';
import { Icon, IconName } from './Icon';

export type InlineMessageTone = 'error' | 'warning' | 'info';

export interface InlineMessageProps {
  /** Renders nothing when empty, so callers can pass error state directly. */
  message?: string | null;
  tone?: InlineMessageTone;
  title?: string;
  /** Optional recovery action, e.g. "Try again". */
  actionLabel?: string;
  onAction?: () => void;
  /** Shows a close button; omit when the message must stay until the state changes. */
  onDismiss?: () => void;
  style?: StyleProp<ViewStyle>;
  testID?: string;
}

const TONES: Record<InlineMessageTone, { fg: keyof ColorTokens; bg: keyof ColorTokens; icon: IconName }> = {
  error: { fg: 'danger', bg: 'dangerBg', icon: 'warning' },
  warning: { fg: 'warning', bg: 'warningBg', icon: 'warning' },
  info: { fg: 'info', bg: 'infoBg', icon: 'info' },
};

export const InlineMessage: React.FC<InlineMessageProps> = ({
  message,
  tone = 'error',
  title,
  actionLabel,
  onAction,
  onDismiss,
  style,
  testID,
}) => {
  const { colors, spacing } = useTheme();
  if (!message) return null;
  const t = TONES[tone];

  return (
    <Animated.View
      entering={FadeIn.duration(160)}
      testID={testID}
      accessibilityRole="alert"
      accessibilityLiveRegion={tone === 'info' ? 'polite' : 'assertive'}
      style={style}
    >
      <AccentCard
        railColor={colors[t.fg]}
        railWidth={3}
        padding="md"
        elevation="none"
        style={{ backgroundColor: colors[t.bg] }}
      >
        <View style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm }}>
          <View style={{ marginTop: 1 }}>
            <Icon name={t.icon} size={18} color={colors[t.fg]} />
          </View>
          <View style={{ flex: 1, gap: spacing.xs }}>
            {title ? (
              <Text variant="bodySm" weight="600" color={t.fg}>
                {title}
              </Text>
            ) : null}
            {/* Selectable: a message may carry a hash or address to copy by hand. */}
            <Text variant="bodySm" weight={title ? '400' : '500'} color={title ? 'textPrimary' : t.fg} selectable>
              {message}
            </Text>
            {actionLabel && onAction ? (
              <Pressable onPress={onAction} accessibilityRole="button" hitSlop={8} style={{ alignSelf: 'flex-start', marginTop: spacing.xs }}>
                <Text variant="bodySm" weight="600" color={t.fg} style={{ textDecorationLine: 'underline' }}>
                  {actionLabel}
                </Text>
              </Pressable>
            ) : null}
          </View>
          {onDismiss ? (
            <Pressable onPress={onDismiss} accessibilityRole="button" accessibilityLabel="Dismiss" hitSlop={10} style={{ padding: 2 }}>
              <Icon name="close" size={18} color={colors[t.fg]} />
            </Pressable>
          ) : null}
        </View>
      </AccentCard>
    </Animated.View>
  );
};
