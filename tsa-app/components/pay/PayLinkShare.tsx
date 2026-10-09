// components/pay/PayLinkShare.tsx
//
// A TSA Pay link, ready to hand to a customer: its QR code, the link itself,
// and Share / Copy link. The caller writes the share message (utils/payLinks —
// the same words the web app sends). Shown by the new-link success step, a
// link's detail screen and the store QR screen (bigger QR, with a heading), and
// by the payer's receipt (app/pay.tsx) as Share receipt and Print / Save PDF.
import React from 'react';
import { Share, View } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Text, useToast, type IconName } from '@/components/ui';
import { QrPanel } from '@/components/common/QrPanel';

export function PayLinkShare({
  url, message, qrSize, children, shareTitle = 'Share', secondary, onShare,
}: {
  url: string;
  message: string;
  qrSize?: number;
  /** Drawn above the QR code (the store QR screen's business name and label). */
  children?: React.ReactNode;
  /** The Share button's words — "Share receipt" on a receipt. */
  shareTitle?: string;
  /** Takes Copy link's place (a receipt offers Print / Save PDF); the two buttons then stack. */
  secondary?: { title: string; icon: IconName; onPress: () => void };
  /** Shares something richer than `message` (a receipt shares its image); `message` stays its fallback. */
  onShare?: () => Promise<void>;
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

  // A receipt's image is fetched before the sheet opens: the button says so meanwhile.
  const [sharing, setSharing] = React.useState(false);
  const share = async () => {
    if (sharing) return;
    setSharing(true);
    try {
      await (onShare ? onShare() : Share.share({ message }));
    } catch (err) {
      console.error('Share error:', err);
    } finally {
      setSharing(false);
    }
  };

  // Side by side, two long labels don't fit a 320 pt card: a receipt's stack.
  const buttonStyle = secondary ? undefined : { flex: 1 };
  return (
    <Card padding="xl" style={{ alignItems: 'center', gap: spacing.md }}>
      {children}
      <QrPanel value={url} size={qrSize} />
      <Text variant="bodySm" color="textSecondary" align="center" selectable>
        {url}
      </Text>
      <View style={{ flexDirection: secondary ? 'column' : 'row', gap: spacing.md, alignSelf: 'stretch' }}>
        <Button title={shareTitle} leftIcon="share" onPress={share} loading={sharing} disabled={sharing} style={buttonStyle} />
        {secondary ? (
          <Button title={secondary.title} variant="secondary" leftIcon={secondary.icon} onPress={secondary.onPress} />
        ) : (
          <Button title="Copy link" variant="secondary" leftIcon="copy" onPress={copy} style={buttonStyle} />
        )}
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
