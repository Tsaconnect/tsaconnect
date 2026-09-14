// The Send receipt: confirmed, still on its way, or failed. Kept out of
// app/wallet/send.tsx so the three states render — and are tested — on their
// own; the screen decides which state applies (utils/sendForm#sendOutcome).
import React, { useState } from 'react';
import { View, ScrollView, Linking } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';
import { useTheme } from '@/theme';
import {
  Badge,
  BottomActionBar,
  Button,
  Card,
  Icon,
  PressableScale,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import { shortAddress } from '@/utils/sendForm';

export type SendResultStatus = 'success' | 'pending' | 'failure';

export interface SendResultProps {
  status: SendResultStatus;
  /** Amount and symbol, e.g. "10 USDT". */
  amountLabel: string;
  recipient: string;
  /** USD value without the currency sign, e.g. "10.00". */
  usdValue: string;
  networkName: string;
  /** Fee with its unit, e.g. "0.0000058 BNB" or "≈ 0.0000058 BNB". */
  feeLabel: string;
  sentAt: Date | null;
  txHash: string;
  explorerUrl: string | null;
  /** Shown when status is 'failure'. */
  error: string;
  onDone: () => void;
  onSendAgain: () => void;
  onTryAgain: () => void;
}

export default function SendResult({
  status,
  amountLabel,
  recipient,
  usdValue,
  networkName,
  feeLabel,
  sentAt,
  txHash,
  explorerUrl,
  error,
  onDone,
  onSendAgain,
  onTryAgain,
}: SendResultProps) {
  const { colors, spacing, radius } = useTheme();
  const [copied, setCopied] = useState(false);

  const ok = status === 'success';
  const pending = status === 'pending';
  const tone = ok
    ? { fg: colors.success, bg: colors.successBg }
    : pending
      ? { fg: colors.warning, bg: colors.warningBg }
      : { fg: colors.danger, bg: colors.dangerBg };

  const copyHash = async () => {
    await Clipboard.setStringAsync(txHash);
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingTop: spacing['3xl'], gap: spacing.lg }}>
        <View style={{ alignItems: 'center', gap: spacing.xs }}>
          <View
            style={{
              width: 72,
              height: 72,
              borderRadius: radius.pill,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.sm,
              backgroundColor: tone.bg,
            }}
          >
            <Icon name={ok ? 'checkCircle' : pending ? 'clock' : 'closeCircle'} size={40} color={tone.fg} />
          </View>
          <Text variant="title2" weight="700" align="center" numeric>
            {ok ? `${amountLabel} sent` : pending ? `Sending ${amountLabel}` : 'Send failed'}
          </Text>
          {ok || pending ? (
            <Text variant="bodySm" color="textSecondary" align="center" numeric>
              to {shortAddress(recipient)}
            </Text>
          ) : null}
        </View>

        {ok || pending ? (
          <Card>
            <SummaryCard>
              <SummaryRow
                label="Status"
                value={<Badge label={ok ? 'Confirmed' : 'On its way'} color={tone.fg} background={tone.bg} size="sm" />}
              />
              <SummaryRow label="Value" value={`≈ $${usdValue}`} />
              <SummaryRow label="Network" value={networkName} />
              <SummaryRow label="Network fee" value={feeLabel} />
              {sentAt ? (
                <SummaryRow
                  label="Time"
                  value={sentAt.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' })}
                />
              ) : null}
            </SummaryCard>
            {txHash ? (
              <PressableScale
                onPress={copyHash}
                accessibilityRole="button"
                accessibilityLabel="Copy transaction hash"
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  justifyContent: 'space-between',
                  gap: spacing.sm,
                  marginTop: spacing.md,
                  paddingHorizontal: spacing.md,
                  paddingVertical: spacing.sm,
                  borderRadius: radius.md,
                  backgroundColor: colors.surfaceAlt,
                }}
              >
                <Text variant="caption" color="textSecondary" numeric numberOfLines={1} ellipsizeMode="middle" style={{ flex: 1 }}>
                  {txHash}
                </Text>
                <Icon name={copied ? 'checkCircle' : 'copy'} size={16} color={copied ? colors.success : colors.accent} />
              </PressableScale>
            ) : null}
          </Card>
        ) : (
          <Card padding="md" style={{ backgroundColor: colors.dangerBg }}>
            <Text variant="bodySm" color="danger" align="center">
              {error || 'Something went wrong. Your funds have not moved.'}
            </Text>
          </Card>
        )}

        {pending ? (
          <View style={{ backgroundColor: colors.surfaceAlt, borderRadius: radius.md, padding: spacing.md }}>
            <Text variant="caption" color="textSecondary">
              It’s on its way. You can leave this screen; the transfer will show in your wallet activity. Don’t send it again.
            </Text>
          </View>
        ) : null}

        {explorerUrl ? (
          <PressableScale onPress={() => Linking.openURL(explorerUrl)} hitSlop={8} style={{ alignSelf: 'center' }}>
            <Text variant="bodySm" weight="600" color="accent">
              View on explorer
            </Text>
          </PressableScale>
        ) : null}
      </ScrollView>

      <BottomActionBar>
        {status === 'failure' ? (
          <>
            <Button title="Try again" size="lg" onPress={onTryAgain} />
            <Button title="Back to wallet" variant="ghost" onPress={onDone} />
          </>
        ) : (
          <>
            <Button title="Done" size="lg" onPress={onDone} />
            {ok ? <Button title="Send again" variant="ghost" onPress={onSendAgain} /> : null}
          </>
        )}
      </BottomActionBar>
    </SafeAreaView>
  );
}
