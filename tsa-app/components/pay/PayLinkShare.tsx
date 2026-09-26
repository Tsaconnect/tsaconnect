// components/pay/PayLinkShare.tsx
//
// A payment link, ready to hand to a customer: its QR code, the link itself,
// and Share / Copy link. The share sheet carries the same message the web app
// sends (utils/payLinks), so a customer reads the same words whichever she used.
// Shown by the new-link success step and a link's detail screen.
import React from 'react';
import { Share, View } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import { AccentCard, Button, Card, Text, useToast } from '@/components/ui';
import { QrPanel } from '@/components/common/QrPanel';
import type { PayLink } from '@/services/payMerchantApi';
import { payLinkShareMessage } from '@/utils/payLinks';

export function PayLinkShare({ link, businessName }: { link: PayLink; businessName: string }) {
  const { spacing } = useTheme();
  const toast = useToast();

  const copy = async () => {
    try {
      await Clipboard.setStringAsync(link.checkoutUrl);
      toast.success('Link copied');
    } catch {
      toast.error('Could not copy');
    }
  };

  const share = async () => {
    try {
      await Share.share({ message: payLinkShareMessage(businessName, link) });
    } catch (err) {
      console.error('Share error:', err);
    }
  };

  return (
    <Card padding="xl" style={{ alignItems: 'center', gap: spacing.md }}>
      <QrPanel value={link.checkoutUrl} />
      <Text variant="bodySm" color="textSecondary" align="center" selectable>
        {link.checkoutUrl}
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
