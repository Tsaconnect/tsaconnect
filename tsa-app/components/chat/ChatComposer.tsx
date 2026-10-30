// The docked message box under a chat: text, or one attached photo (uploaded
// to the platform CDN on send). The P2P trade thread and every conversation
// use it. The parent sends; a send that fails restores what was typed.
import * as ImagePicker from 'expo-image-picker';
import React, { useState } from 'react';
import { Alert, Image, TextInput, View } from 'react-native';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Icon, IconButton, PressableScale } from '@/components/ui';
import { uploadImageFile } from '@/services/imageUpload';

export type ChatOutgoing = { text: string } | { imageUrl: string };

interface ChatComposerProps {
  /** Sends one message; resolves true when it was accepted. */
  onSend: (msg: ChatOutgoing) => Promise<boolean>;
  /** Blocks typing and sending (e.g. while the parent runs another action). */
  disabled?: boolean;
  /** Prefix for uploaded photo file names. */
  uploadName?: string;
}

export const ChatComposer: React.FC<ChatComposerProps> = ({ onSend, disabled, uploadName = 'chat' }) => {
  const { colors, spacing } = useTheme();
  // The composer is docked below the scroll area, so it clears the phone's
  // gesture bar / nav buttons itself.
  const insets = useSafeAreaInsets();
  const [text, setText] = useState('');
  const [image, setImage] = useState<string | null>(null);
  const [sending, setSending] = useState(false);

  const blocked = !!disabled || sending;
  const canSend = !blocked && (!!image || !!text.trim());

  const pickImage = async () => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== 'granted') {
      Alert.alert('Permission required', 'Allow photo library access to attach images.');
      return;
    }
    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      allowsEditing: false,
      quality: 0.8,
    });
    if (!result.canceled && result.assets?.[0]) setImage(result.assets[0].uri);
  };

  const send = async () => {
    if (!canSend) return;
    setSending(true);
    try {
      if (image) {
        const up = await uploadImageFile(image, uploadName);
        if (!('url' in up)) {
          Alert.alert('Upload failed', up.error);
          return;
        }
        if (await onSend({ imageUrl: up.url })) setImage(null);
        return;
      }
      const body = text.trim();
      setText('');
      if (!(await onSend({ text: body }))) setText(body); // keep what they typed
    } finally {
      setSending(false);
    }
  };

  return (
    <View
      style={{
        flexDirection: 'row',
        alignItems: 'flex-end',
        gap: spacing.sm,
        paddingHorizontal: spacing.md,
        paddingTop: spacing.sm,
        paddingBottom: spacing.sm + insets.bottom,
        backgroundColor: colors.surface,
        borderTopWidth: 1,
        borderTopColor: colors.hairline,
      }}
    >
      {image ? (
        <View style={{ flex: 1, position: 'relative' }}>
          <Image source={{ uri: image }} style={{ width: 80, height: 80, borderRadius: 8 }} />
          <PressableScale
            onPress={() => setImage(null)}
            accessibilityLabel="Remove attached image"
            style={{
              position: 'absolute',
              top: 4,
              left: 60,
              backgroundColor: colors.overlay,
              borderRadius: 12,
              width: 24,
              height: 24,
              alignItems: 'center',
              justifyContent: 'center',
            }}
          >
            <Icon name="close" size={16} color={colors.textInverse} />
          </PressableScale>
        </View>
      ) : (
        <View
          style={{
            flex: 1,
            maxHeight: 120,
            justifyContent: 'center',
            backgroundColor: colors.surfaceAlt,
            borderRadius: 20,
            paddingHorizontal: spacing.md,
          }}
        >
          {/* minHeight on the TextInput (not the wrapper) makes the WHOLE pill
              a tap target. */}
          <TextInput
            value={text}
            onChangeText={setText}
            placeholder="Type a message…"
            placeholderTextColor={colors.textMuted}
            multiline
            editable={!blocked}
            style={{
              color: colors.textPrimary,
              fontSize: 14,
              minHeight: 40,
              paddingVertical: spacing.sm,
              textAlignVertical: 'center',
            }}
          />
        </View>
      )}
      <IconButton
        icon="image"
        accessibilityLabel="Attach image"
        color={colors.accent}
        disabled={blocked}
        onPress={pickImage}
      />
      <IconButton
        icon="send"
        variant="surface"
        accessibilityLabel="Send message"
        color={canSend ? colors.onAccent : colors.textMuted}
        disabled={!canSend}
        onPress={send}
        style={{ backgroundColor: canSend ? colors.accent : colors.surfaceAlt }}
      />
    </View>
  );
};
