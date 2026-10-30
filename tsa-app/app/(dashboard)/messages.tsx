// app/(dashboard)/messages.tsx
//
// Messages — every conversation the user takes part in: as a customer (with
// providers whose contact fee they paid) and as a provider (once a customer
// writes). Newest activity first; unread ones are marked. Tapping opens the
// conversation (/chat/<id>).
import React, { useCallback, useState } from 'react';
import { Image, RefreshControl, ScrollView, View } from 'react-native';
import { router, useFocusEffect } from 'expo-router';
import { useTheme } from '@/theme';
import { EmptyState, Icon, PressableScale, Spinner, Text } from '@/components/ui';
import { chatApi, type ChatThread } from '@/services/chatApi';
import { formatDate } from '@/constants/orderStatus';

export default function MessagesScreen() {
  const { colors, spacing, radius } = useTheme();
  const [threads, setThreads] = useState<ChatThread[]>([]);
  const [loading, setLoading] = useState(true);
  const [refreshing, setRefreshing] = useState(false);

  const load = useCallback(async () => {
    const res = await chatApi.threads();
    if (res.success && res.data) setThreads(res.data);
    setLoading(false);
    setRefreshing(false);
  }, []);

  // Reload on every return, so a thread just read loses its unread mark.
  useFocusEffect(
    useCallback(() => {
      load();
    }, [load]),
  );

  if (loading) {
    return (
      <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.background }}>
        <Spinner />
      </View>
    );
  }

  return (
    <ScrollView
      style={{ flex: 1, backgroundColor: colors.background }}
      contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm, flexGrow: 1 }}
      refreshControl={
        <RefreshControl
          refreshing={refreshing}
          onRefresh={() => {
            setRefreshing(true);
            load();
          }}
        />
      }
    >
      {threads.length === 0 ? (
        <EmptyState
          icon="chat"
          title="No messages yet"
          subtitle="After you pay to contact a provider, you can chat with them here."
        />
      ) : (
        threads.map((t) => (
          <PressableScale
            key={t.id}
            onPress={() => router.push(`/chat/${t.id}`)}
            accessibilityRole="button"
            accessibilityLabel={`Chat with ${t.counterpartName}`}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              gap: spacing.md,
              padding: spacing.md,
              borderRadius: radius.lg,
              borderWidth: 1,
              borderColor: t.unread ? colors.accent : colors.border,
              backgroundColor: colors.surface,
            }}
          >
            {t.subjectImage ? (
              <Image source={{ uri: t.subjectImage }} style={{ width: 44, height: 44, borderRadius: radius.md }} />
            ) : (
              <View
                style={{
                  width: 44,
                  height: 44,
                  borderRadius: radius.md,
                  backgroundColor: colors.surfaceAlt,
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <Icon name="chat" size={20} color={colors.textMuted} />
              </View>
            )}
            <View style={{ flex: 1, gap: 2 }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Text variant="bodySm" weight={t.unread ? '700' : '600'} numberOfLines={1} style={{ flex: 1 }}>
                  {t.counterpartName || 'Conversation'}
                </Text>
                {t.lastMessageAt ? (
                  <Text variant="caption" color="textMuted">
                    {formatDate(t.lastMessageAt)}
                  </Text>
                ) : null}
              </View>
              <Text variant="caption" color="textMuted" numberOfLines={1}>
                {t.role === 'provider' ? 'Your service · ' : ''}
                {t.subjectTitle}
              </Text>
              <Text
                variant="caption"
                color={t.unread ? 'textPrimary' : 'textSecondary'}
                weight={t.unread ? '600' : undefined}
                numberOfLines={1}
              >
                {t.lastMessagePreview || 'No messages yet'}
              </Text>
            </View>
            {t.unread ? (
              <View style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: colors.accent }} />
            ) : null}
          </PressableScale>
        ))
      )}
    </ScrollView>
  );
}
