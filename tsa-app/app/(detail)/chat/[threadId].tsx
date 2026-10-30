// app/(detail)/chat/[threadId].tsx
//
// One customer–provider conversation. Opened from a contact service once its
// fee is paid, from the Messages drawer screen, or from a message push.
// Refresh is the realtime frame for this thread (fast path) plus a slow poll
// while focused — frames are best-effort and are not replayed after a
// disconnect. Reading the thread marks it read.
import { Stack, useLocalSearchParams } from 'expo-router';
import { useIsFocused } from '@react-navigation/native';
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Alert, KeyboardAvoidingView, Platform, ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { EmptyState, Spinner, Text } from '@/components/ui';
import { ChatComposer, type ChatOutgoing } from '@/components/chat/ChatComposer';
import { ChatMessageBubble } from '@/components/chat/ChatMessageBubble';
import { useAuth } from '@/AuthContext/AuthContext';
import { useNotifications } from '@/contexts/NotificationContext';
import { chatApi, type ChatMessage, type ChatThread } from '@/services/chatApi';
import { isChatFrameFor } from '@/utils/chatFrame';
import { p2pErrorMessage } from '@/services/p2pErrors';

const SAFETY_POLL_MS = 20_000;

export default function ChatThreadScreen() {
  const { colors, spacing } = useTheme();
  const { threadId } = useLocalSearchParams<{ threadId: string }>();
  const { currentUser } = useAuth();
  const myId: string | undefined = currentUser?.id;
  const { subscribe, connected } = useNotifications();
  const isFocused = useIsFocused();

  const [thread, setThread] = useState<ChatThread | null>(null);
  const [messages, setMessages] = useState<ChatMessage[]>([]);
  const [loading, setLoading] = useState(true);
  // 'missing': the server refused the thread (bad or not-yours id);
  // 'failed': it could not be loaded at all (network/server) and none is shown.
  const [loadError, setLoadError] = useState<'missing' | 'failed' | null>(null);
  const scrollRef = useRef<ScrollView>(null);
  const messagesRef = useRef<ChatMessage[]>([]);
  messagesRef.current = messages;

  const id = threadId ? String(threadId) : '';

  const load = useCallback(async () => {
    if (!id) return;
    const [t, m] = await Promise.all([chatApi.thread(id), chatApi.messages(id)]);
    // A refusal (4xx) is "not found"; a network or server error keeps what is
    // already on screen, and the next load retries.
    if (t.success && t.data) {
      setThread(t.data);
      setLoadError(null);
    } else {
      const refused = (t.status ?? 0) >= 400 && (t.status ?? 0) < 500;
      setLoadError((prev) => (refused ? 'missing' : prev === 'missing' ? prev : 'failed'));
    }
    // Merge by id: a message pulled or sent while this request was in flight
    // must not be dropped by the older snapshot.
    if (m.success && m.data) setMessages((prev) => mergeById(m.data!, prev));
    setLoading(false);
    if (t.success) chatApi.markRead(id);
  }, [id]);

  // Pull only what arrived after the last message we hold.
  const pullNew = useCallback(async () => {
    if (!id) return;
    const list = messagesRef.current;
    const since = list.length ? list[list.length - 1].createdAt : undefined;
    const res = await chatApi.messages(id, since);
    // since is inclusive, so the last message we hold comes back too: only
    // genuinely new ones count, and only they mark the thread read.
    const seen = new Set(messagesRef.current.map((m) => m.id));
    const fresh = res.success && res.data ? res.data.filter((m) => !seen.has(m.id)) : [];
    if (fresh.length) {
      setMessages((prev) => mergeById(prev, fresh));
      chatApi.markRead(id);
    }
  }, [id]);

  useEffect(() => {
    if (!isFocused) return;
    load();
    const t = setInterval(pullNew, SAFETY_POLL_MS);
    return () => clearInterval(t);
  }, [isFocused, load, pullNew]);

  useEffect(() => {
    if (!isFocused || !id) return;
    return subscribe((frame) => {
      if (isChatFrameFor(frame, id)) pullNew();
    });
  }, [isFocused, id, subscribe, pullNew]);

  // A reconnect replays nothing, so catch up once.
  useEffect(() => {
    if (connected && isFocused) pullNew();
    // Only on the `connected` edge.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [connected]);

  useEffect(() => {
    setTimeout(() => scrollRef.current?.scrollToEnd({ animated: true }), 80);
  }, [messages.length]);

  const send = async (msg: ChatOutgoing): Promise<boolean> => {
    const res =
      'imageUrl' in msg ? await chatApi.sendImage(id, msg.imageUrl) : await chatApi.sendText(id, msg.text);
    if (res.success && res.data) {
      setMessages((prev) => (prev.some((m) => m.id === res.data!.id) ? prev : [...prev, res.data!]));
      return true;
    }
    Alert.alert('Send failed', p2pErrorMessage(res));
    return false;
  };

  const title = thread?.counterpartName || 'Chat';

  if (loading) {
    return (
      <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center', backgroundColor: colors.background }}>
        <Stack.Screen options={{ title }} />
        <Spinner />
      </View>
    );
  }
  if (loadError === 'missing' || (loadError === 'failed' && !thread)) {
    return (
      <View style={{ flex: 1, backgroundColor: colors.background, padding: spacing.lg }}>
        <Stack.Screen options={{ title }} />
        {loadError === 'missing' ? (
          <EmptyState icon="chat" title="Conversation not found" subtitle="It may have been removed, or it isn't yours." />
        ) : (
          <EmptyState
            icon="chat"
            title="Couldn't load this conversation"
            subtitle="Check your connection and try again."
            actionLabel="Try again"
            onAction={load}
          />
        )}
      </View>
    );
  }

  return (
    <KeyboardAvoidingView
      style={{ flex: 1, backgroundColor: colors.background }}
      behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      keyboardVerticalOffset={Platform.OS === 'ios' ? 90 : 0}
    >
      <Stack.Screen options={{ title }} />
      {thread?.subjectTitle ? (
        <View
          style={{
            paddingHorizontal: spacing.lg,
            paddingVertical: spacing.sm,
            borderBottomWidth: 1,
            borderBottomColor: colors.hairline,
            backgroundColor: colors.surface,
          }}
        >
          <Text variant="caption" color="textMuted" numberOfLines={1}>
            {thread.role === 'customer' ? 'About their service: ' : 'About your service: '}
            {thread.subjectTitle}
          </Text>
        </View>
      ) : null}
      <ScrollView
        ref={scrollRef}
        // Messages sit at the bottom, next to the box: Android pans the screen
        // up for the keyboard, and the latest messages must stay in view.
        contentContainerStyle={{ padding: spacing.md, gap: spacing.sm, flexGrow: 1, justifyContent: 'flex-end' }}
        keyboardShouldPersistTaps="handled"
      >
        {messages.length === 0 ? (
          <Text variant="bodySm" color="textMuted" align="center" style={{ paddingVertical: spacing.xl }}>
            {thread?.role === 'customer'
              ? 'Say hello and describe what you need.'
              : 'No messages yet.'}
          </Text>
        ) : (
          messages.map((m) => <ChatMessageBubble key={m.id} message={m} isMine={m.senderId === myId} />)
        )}
      </ScrollView>
      <ChatComposer onSend={send} uploadName="chat" />
    </KeyboardAvoidingView>
  );
}

// Messages from both lists, each once, oldest first.
function mergeById(a: ChatMessage[], b: ChatMessage[]): ChatMessage[] {
  const byId = new Map<string, ChatMessage>();
  for (const m of [...a, ...b]) byId.set(m.id, m);
  return [...byId.values()].sort((x, y) => x.createdAt.localeCompare(y.createdAt) || x.id.localeCompare(y.id));
}
