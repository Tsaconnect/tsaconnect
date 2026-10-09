// The buyer's payment proof (R10): the seller sees it next to Release; both see
// it while disputed. Tap opens the full image. The URL is user-supplied, so only
// https is ever loaded (as a plain image, never HTML/webview).
import React from 'react';
import { Image, Linking } from 'react-native';
import { useTheme } from '@/theme';
import { Card, PressableScale, Text, useToast } from '@/components/ui';
import { safeProofUrl } from '@/utils/payInto';

export const PaymentProof: React.FC<{ url: string; mine: boolean }> = ({ url, mine }) => {
  const { spacing, radius } = useTheme();
  const toast = useToast();
  const safeUrl = safeProofUrl(url);
  if (!safeUrl) return null;
  return (
    <Card style={{ gap: spacing.sm }}>
      <Text variant="bodySm" weight="700">{mine ? 'Your payment proof' : "Buyer's payment proof"}</Text>
      <PressableScale
        accessibilityRole="imagebutton"
        accessibilityLabel="Open payment proof"
        onPress={() => Linking.openURL(safeUrl).catch(() => toast.error('Could not open the image'))}
      >
        <Image source={{ uri: safeUrl }} style={{ width: '100%', height: 180, borderRadius: radius.md }} resizeMode="cover" />
      </PressableScale>
      {!mine && (
        <Text variant="caption" color="textMuted">
          A screenshot is not proof the money arrived. Check your own account before releasing.
        </Text>
      )}
    </Card>
  );
};
