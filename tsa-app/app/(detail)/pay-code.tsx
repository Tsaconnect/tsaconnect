// app/(detail)/pay-code.tsx
//
// TSA Pay, the payer's hub. She types or pastes the TSA-XXXXX-XXXXX code a
// business's page or printed invoice shows — or pastes its pay link — and lands
// on the ordinary checkout (/pay?id=…), which handles every status. Below that:
// payments she opened and has not paid yet (kept on this phone), her recent
// TSA Pay payments (the activity feed, payments only) and, for a TSA Pay
// merchant, the way to get paid. Reached from Home's Pay action and the
// wallet's TSA Pay tile. The field has no maxLength on purpose: a pasted
// "TSA-…" code is fifteen characters and must arrive whole to be read.
//
// The clipboard is read only when she taps Paste: reading it on open would
// raise the system's paste prompt (iOS) or toast (Android) every visit.
import React, { useCallback, useRef, useState } from 'react';
import { ScrollView, View } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { router, useFocusEffect } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Card, Icon, Input, PressableScale, Text } from '@/components/ui';
import ActivityFeed from '@/components/activity/ActivityFeed';
import { PayLinkEntry } from '@/components/pay/PayLinkEntry';
import { useOwnPayMerchant } from '@/hooks/useOwnPayMerchant';
import { getCheckout, lookupPayCode } from '@/services/payApi';
import { dropTypedPrefix, normalizePayCode, parsePayInput, payCodeFieldText, payCodeLookupError } from '@/utils/payCode';
import { forgetPayCheckouts, listPayCheckouts, type RecentPayCheckout } from '@/utils/recentPayCheckouts';
import { formatKobo } from '@/utils/stableSellQuote';
import { formatLinkTime } from '@/utils/payLinks';

const PAY_LINK_NOT_SUPPORTED = "That isn't a TSA Pay code or payment link.";

export default function PayCodeScreen() {
  const { colors, spacing } = useTheme();
  const [text, setText] = useState('');
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const code = normalizePayCode(text);
  // `busy` is state, so two calls in one frame both see it false: this ref is the real guard.
  const inFlight = useRef(false);

  const edit = (value: string) => {
    setText(payCodeFieldText(dropTypedPrefix(value)));
    setError(undefined);
  };

  const open = async (bare: string) => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError(undefined);
    try {
      const id = await lookupPayCode(bare);
      router.push(`/pay?id=${id}` as any);
    } catch (err) {
      setError(payCodeLookupError(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  // A pasted code or one-time pay link goes straight on; anything else lands
  // in the field as typed, where the code rules say what is wrong with it.
  const paste = async () => {
    let pasted: string;
    try {
      pasted = await Clipboard.getStringAsync();
    } catch {
      return; // Nothing readable on the clipboard: the field stays as it was.
    }
    const input = parsePayInput(pasted);
    if (input?.kind === 'payment') {
      router.push(`/pay?id=${input.id}` as any);
      return;
    }
    edit(pasted);
    if (input?.kind === 'code') void open(input.code);
    else if (/^\s*(https?|tsaconnect):/i.test(pasted)) setError(PAY_LINK_NOT_SUPPORTED);
  };

  const submit = () => {
    if (code) void open(code);
  };

  return (
    <ScrollView
      style={{ flex: 1, backgroundColor: colors.background }}
      contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg, paddingBottom: spacing['4xl'] }}
      keyboardShouldPersistTaps="handled"
    >
      <Text variant="bodySm" color="textSecondary">
        Enter the payment code the business gave you — it looks like TSA-7K2QF-M9XWD — or paste their payment link.
      </Text>
      <Input
        label="Payment code"
        value={text}
        onChangeText={edit}
        placeholder="XXXXX-XXXXX"
        autoCapitalize="characters"
        autoCorrect={false}
        autoComplete="off"
        returnKeyType="go"
        onSubmitEditing={() => void submit()}
        error={error}
        leftAdornment={<Text variant="body" color="textSecondary">TSA-</Text>}
        rightAdornment={<Button title="Paste" variant="ghost" size="sm" onPress={paste} />}
      />
      <Button title="Continue" size="lg" onPress={submit} loading={busy} disabled={!code || busy} />
      <WaitingForYou />
      <RecentPayments />
      <GetPaid />
    </ScrollView>
  );
}

function SectionTitle({ children }: { children: string }) {
  return (
    <Text variant="bodyMed" weight="700">
      {children}
    </Text>
  );
}

/** expiresAt null: the payment never expires — the row says only that it isn't paid. */
type Waiting = RecentPayCheckout & { expiresAt?: string | null };

/**
 * Payments she opened on this phone and can still pay. Each is re-read from the
 * server on every visit; one that is paid, ended or no longer hers to pay is
 * forgotten, and one that can't be read right now keeps its remembered row.
 */
function WaitingForYou() {
  const { colors, spacing } = useTheme();
  const [items, setItems] = useState<Waiting[]>([]);

  useFocusEffect(
    useCallback(() => {
      let live = true;
      (async () => {
        const remembered = await listPayCheckouts();
        const gone: string[] = [];
        const read = await Promise.all(
          remembered.map(async (r): Promise<Waiting | null> => {
            try {
              const c = await getCheckout(r.id);
              if (c.status === 'pending' && c.payable && !c.youArePaying) {
                return { ...r, merchantName: c.merchantName, customerTotal: c.customerTotal, expiresAt: c.expiresAt };
              }
              gone.push(r.id);
              return null;
            } catch (e: any) {
              if (e?.status === 404) {
                gone.push(r.id);
                return null;
              }
              return r;
            }
          }),
        );
        // One write for all of them: forgetting each in parallel would race.
        void forgetPayCheckouts(gone);
        if (live) setItems(read.filter((x): x is Waiting => x !== null));
      })();
      return () => { live = false; };
    }, []),
  );

  if (items.length === 0) return null;
  return (
    <View style={{ gap: spacing.sm }}>
      <SectionTitle>Waiting for you</SectionTitle>
      {items.map((w) => (
        <PressableScale
          key={w.id}
          onPress={() => router.push(`/pay?id=${w.id}` as any)}
          accessibilityRole="button"
          accessibilityLabel={`Pay ${w.merchantName} ${formatKobo(String(w.customerTotal))}`}
        >
          <Card padding="md">
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <Icon name="clock" size={20} color={colors.accent} />
              <View style={{ flex: 1 }}>
                <Text variant="bodyMed" weight="600">{w.merchantName}</Text>
                <Text variant="caption" color="textSecondary">
                  {w.expiresAt ? `Open until ${formatLinkTime(w.expiresAt)}` : 'Not paid yet'}
                </Text>
              </View>
              <Text variant="bodyMed" weight="700">{formatKobo(String(w.customerTotal))}</Text>
              <Icon name="chevronRight" size={18} color={colors.textMuted} />
            </View>
          </Card>
        </PressableScale>
      ))}
    </View>
  );
}

/** Her TSA Pay payments, newest first: the activity feed, payments only, re-read on every visit. */
function RecentPayments() {
  const { spacing } = useTheme();
  // Remounted on each return to the screen, never on the first focus (the mount already read it).
  const [visit, setVisit] = useState(0);
  const firstFocus = useRef(true);
  useFocusEffect(useCallback(() => {
    if (firstFocus.current) {
      firstFocus.current = false;
      return;
    }
    setVisit((v) => v + 1);
  }, []));
  return (
    <View style={{ gap: spacing.sm }}>
      <SectionTitle>Recent payments</SectionTitle>
      <Card padding="xs">
        <ActivityFeed
          key={visit}
          types={['payment']}
          embedded
          onSeeAll={() => router.push('/wallet/history?types=payment' as any)}
        />
      </Card>
    </View>
  );
}

/** A TSA Pay merchant's way to get paid, beside paying: the same entry her dashboard shows. */
function GetPaid() {
  const { spacing } = useTheme();
  const { load } = useOwnPayMerchant();
  if (load.kind !== 'ready') return null;
  return (
    <View style={{ gap: spacing.sm }}>
      <SectionTitle>Get paid</SectionTitle>
      <PayLinkEntry canRequest={load.merchant.status !== 'suspended'} />
    </View>
  );
}
