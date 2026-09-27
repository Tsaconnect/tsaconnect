// components/pay/PayLinkShare.tsx
//
// A TSA Pay link, ready to hand to a customer: its QR code, the link itself,
// and Share / Copy link. The caller writes the share message (utils/payLinks —
// the same words the web app sends). Shown by the new-link success step, a
// link's detail screen and the store QR screen (bigger QR, with a heading).
import React from 'react';
import { Share, View } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Text, useToast } from '@/components/ui';
import { QrPanel } from '@/components/common/QrPanel';

export function PayLinkShare({
  url, message, qrSize, children,
}: {
  url: string;
  message: string;
  qrSize?: number;
  /** Drawn above the QR code (the store QR screen's business name and label). */
  children?: React.ReactNode;
}) {
  const { spacing } = useTheme();
  const toast = useToast();

  const copy = async () => {
    try {
      await Clipboard.setStringAsync(url);
      toast.success('Link copied');
    } catch {
      toast.error('Could not copy');
    }
  };

  const share = async () => {
    try {
      await Share.share({ message });
    } catch (err) {
      console.error('Share error:', err);
    }
  };

  return (
    <Card padding="xl" style={{ alignItems: 'center', gap: spacing.md }}>
      {children}
      <QrPanel value={url} size={qrSize} />
      <Text variant="bodySm" color="textSecondary" align="center" selectable>
        {url}
      </Text>
      <View style={{ flexDirection: 'row', gap: spacing.md, alignSelf: 'stretch' }}>
        <Button title="Share" leftIcon="share" onPress={share} style={{ flex: 1 }} />
        <Button title="Copy link" variant="secondary" leftIcon="copy" onPress={copy} style={{ flex: 1 }} />
      </View>
    </Card>
  );
}

/** Shown on every link screen in test mode: a test link moves no money. */
export function TestLinkBanner() {
  const { colors } = useTheme();
  return (
    <AccentCard railColor={colors.warning} padding="md" style={{ backgroundColor: colors.warningBg }}>
      <Text variant="bodySm" weight="700" color="warning">
        TEST LINK — no money moves
      </Text>
      <Text variant="caption" color="textSecondary">
        A test link runs the whole flow for you and your customer, but nothing is charged.
      </Text>
    </AccentCard>
  );
}
