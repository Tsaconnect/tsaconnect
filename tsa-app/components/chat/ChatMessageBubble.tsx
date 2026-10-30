// One chat message — the P2P trade thread's and every conversation's.
// User messages sit right (mine) or left (theirs); system/bot rows are a
// centred italic note.
import React from 'react';
import { Image, View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';

export interface ChatBubbleMessage {
  id: string;
  type: string; // text | image | system | bot
  body?: string;
  imageUrl?: string;
  createdAt: string;
}

export function formatChatTime(iso: string): string {
  const d = new Date(iso);
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
}

export const ChatMessageBubble: React.FC<{ message: ChatBubbleMessage; isMine: boolean }> = ({
  message,
  isMine,
}) => {
  const { colors, spacing, radius } = useTheme();
  if (message.type === 'system' || message.type === 'bot') {
    return (
      <View
        style={{
          alignSelf: 'center',
          backgroundColor: colors.surfaceAlt,
          paddingHorizontal: spacing.md,
          paddingVertical: spacing.xs,
          borderRadius: radius.md,
          maxWidth: '90%',
          marginVertical: spacing.xs,
        }}
      >
        <Text variant="caption" color="textSecondary" align="center" style={{ fontStyle: 'italic' }}>
          {message.body}
        </Text>
      </View>
    );
  }
  return (
    <View
      style={{
        maxWidth: '85%',
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.sm,
        borderRadius: radius.lg,
        alignSelf: isMine ? 'flex-end' : 'flex-start',
        backgroundColor: isMine ? colors.accent : colors.surface,
        borderWidth: isMine ? 0 : 1,
        borderColor: colors.hairline,
        borderBottomRightRadius: isMine ? radius.sm : radius.lg,
        borderBottomLeftRadius: isMine ? radius.lg : radius.sm,
      }}
    >
      {message.type === 'image' && message.imageUrl ? (
        <Image
          source={{ uri: message.imageUrl }}
          style={{ width: 200, height: 200, borderRadius: radius.sm }}
        />
      ) : (
        <Text variant="bodySm" style={{ color: isMine ? colors.onAccent : colors.textPrimary }}>
          {message.body}
        </Text>
      )}
      <Text
        variant="caption"
        style={{ marginTop: spacing.xs, color: isMine ? colors.onAccent : colors.textMuted }}
      >
        {formatChatTime(message.createdAt)}
      </Text>
    </View>
  );
};
