// Presentational pieces of the fixed-currency liquidity screen (app/liquidity/fiat.tsx).
// Pure display: all state and the deposit safety logic stay in the screen.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import {
  AccentCard, Badge, Button, Card, Divider, Icon, PressableScale, Sheet, Skeleton, SummaryRow, Text, useToneColors,
  type StatusTone,
} from '@/components/ui';
import type { FiatCurrency, FiatHolding, FiatPosition } from '@/services/liquidityApi';
import {
  baseUnitsToAmount, fiatActivity, formatCurrency, formatLiquidityAmount, isZeroAmount, type FiatActivityItem,
} from '@/utils/liquidityView';

const WITHDRAWAL_STATUS: Record<string, { label: string; tone: StatusTone }> = {
  requested: { label: 'Waiting to be paid', tone: 'warning' },
  paid: { label: 'Paid', tone: 'success' },
  rejected: { label: 'Declined', tone: 'danger' },
};

function Pill({ label, tone }: { label: string; tone: StatusTone }) {
  const { color, bg } = useToneColors(tone);
  return <Badge label={label} color={color} background={bg} size="sm" />;
}

const shortDate = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
};

/** Entry card on the liquidity screen. */
export function FiatEntryCard({ onPress }: { onPress: () => void }) {
  const { colors, spacing } = useTheme();
  return (
    <PressableScale onPress={onPress} accessibilityRole="button" accessibilityLabel="Fixed-value liquidity. Open.">
      <AccentCard padding="md">
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
          <Icon name="lock" size={24} color={colors.accent} />
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="700">Fixed-value liquidity</Text>
            <Text variant="caption" color="textSecondary">
              Add USDT, get back a fixed amount in naira, shillings or cedis. No exchange-rate risk.
            </Text>
          </View>
          <Icon name="chevronRight" size={20} color={colors.textMuted} />
        </View>
      </AccentCard>
    </PressableScale>
  );
}

export type HoldAction = 'add' | 'withdraw';

const HOLD_COPY: Record<HoldAction, { title: string; usdt: string; fixed: string }> = {
  add: {
    title: 'How do you want to add?',
    usdt: 'Earns the pool share. Your balance moves with the dollar. Withdraw yourself after a 24-hour lock.',
    fixed: "Locked at today's rate, so you carry no exchange-rate risk. Exits are paid by the TSA team, not instantly.",
  },
  withdraw: {
    title: 'What are you withdrawing?',
    usdt: 'Pool liquidity, sent straight to your wallet.',
    fixed: "A fixed-value balance. Request it and the TSA team pays it in USDT at the day's rate.",
  },
};

function HoldOption({ icon, label, sublabel, note, disabled, onPress }: {
  icon: 'pool' | 'lock'; label: string; sublabel: string; note?: string; disabled?: boolean; onPress: () => void;
}) {
  const { colors, radius, spacing } = useTheme();
  return (
    <View style={{ gap: spacing.xs }}>
      <PressableScale onPress={onPress} disabled={disabled} accessibilityRole="button" accessibilityLabel={`${label}. ${sublabel}`}
        style={{
          flexDirection: 'row', alignItems: 'center', gap: spacing.md, padding: spacing.md, borderRadius: radius.md,
          borderWidth: 1.5, borderColor: colors.border, backgroundColor: colors.surface, opacity: disabled ? 0.5 : 1,
        }}>
        <Icon name={icon} size={22} color={disabled ? colors.textMuted : colors.accent} />
        <View style={{ flex: 1 }}>
          <Text variant="bodyMed" weight="600">{label}</Text>
          <Text variant="caption" color="textSecondary">{sublabel}</Text>
        </View>
        <Icon name="chevronRight" size={18} color={colors.textMuted} />
      </PressableScale>
      {note ? <Text variant="caption" color="textMuted" style={{ paddingHorizontal: spacing.xs }}>{note}</Text> : null}
    </View>
  );
}

/**
 * The first step of Add and Withdraw: hold it as pool USDT, or as a fixed value
 * in a currency. `usdtNote` says why the USDT route is unavailable right now.
 */
export function HoldTypeSheet({ visible, action, usdtDisabled, usdtNote, onClose, onUsdt, onFixed }: {
  visible: boolean; action: HoldAction; usdtDisabled?: boolean; usdtNote?: string;
  onClose: () => void; onUsdt: () => void; onFixed: () => void;
}) {
  const { spacing } = useTheme();
  const copy = HOLD_COPY[action];
  return (
    <Sheet visible={visible} onClose={onClose} title={copy.title}>
      <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.md, paddingBottom: spacing.lg, gap: spacing.md }}>
        <HoldOption icon="pool" label="USDT" sublabel={copy.usdt} disabled={usdtDisabled} note={usdtDisabled ? usdtNote : undefined} onPress={onUsdt} />
        <HoldOption icon="lock" label="Fixed in your currency" sublabel={copy.fixed} onPress={onFixed} />
      </View>
    </Sheet>
  );
}

/** Total locked value, one line per currency. */
export function FiatHero({ holdings, currencies }: { holdings: FiatHolding[]; currencies: FiatCurrency[] }) {
  const { spacing } = useTheme();
  const single = holdings.length === 1;
  return (
    <AccentCard padding="lg" style={{ gap: spacing.sm }}>
      <Text variant="bodySm" color="textSecondary">Locked for you</Text>
      {holdings.map((h) => (
        <Text key={h.currency} variant={single ? 'display' : 'title1'} numeric
          accessibilityLabel={`${formatCurrency(h.principal, h.currency, currencies)} locked`}>
          {formatCurrency(h.principal, h.currency, currencies)}
        </Text>
      ))}
      <Text variant="bodySm" color="textSecondary">
        {holdings.length > 1 ? 'These amounts are' : 'This amount is'} fixed. Whatever the dollar does, you get back exactly {holdings.length > 1 ? 'these amounts' : 'this'}, paid in USDT at the day&apos;s rate.
      </Text>
    </AccentCard>
  );
}

/** One currency: what is locked, the rate then and now, and what an exit pays today. */
export function FiatPositionCard({ holding, currencies, sym }: { holding: FiatHolding; currencies: FiatCurrency[]; sym: string }) {
  const { spacing } = useTheme();
  const cur = currencies.find((c) => c.code === holding.currency);
  const money = (v?: string) => formatCurrency(v, holding.currency, currencies);
  return (
    <Card padding="md">
      <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between', marginBottom: spacing.xs }}>
        <View style={{ flex: 1 }}>
          <Text variant="bodyMed" weight="700">{holding.currency}</Text>
          {cur && <Text variant="caption" color="textMuted">{cur.name}</Text>}
        </View>
        <Text variant="title3" numeric>{money(holding.principal)}</Text>
      </View>
      <Divider />
      <SummaryRow label="USDT behind it" value={`${formatLiquidityAmount(holding.units)} ${sym}`} />
      {holding.avgRate && <SummaryRow label="Rate when added" value={`${money(holding.avgRate)} per $1`} />}
      {holding.currentRate && <SummaryRow label="Rate today" value={`${money(holding.currentRate)} per $1`} />}
      {holding.todayUsdt && (
        <SummaryRow label="Paid out today" value={`${formatLiquidityAmount(holding.todayUsdt)} ${sym}`}
          hint="What an exit would pay in USDT at today's rate." />
      )}
    </Card>
  );
}

export function FiatEarningsCard({ pos, sym, busy, onWithdraw }: { pos: FiatPosition; sym: string; busy: boolean; onWithdraw: () => void }) {
  const { spacing } = useTheme();
  const values = Object.entries(pos.earningsValue ?? {}).filter(([code]) => pos.positions.some((h) => h.currency === code));
  return (
    <Card padding="md" style={{ gap: spacing.xs }}>
      <Text variant="bodyMed" weight="700">Earnings</Text>
      <Text variant="title2" color="success" numeric>{formatLiquidityAmount(pos.earningsAvailable)} {sym}</Text>
      {values.length > 0 && (
        <Text variant="bodySm" color="textSecondary">
          About {values.map(([code, v]) => formatCurrency(v, code, pos.currencies)).join(' / ')} at today&apos;s rate.
        </Text>
      )}
      <Text variant="caption" color="textMuted">
        Your USDT earns the same pool share as any other provider. Earnings are paid in USDT.
      </Text>
      <Button title="Withdraw earnings" variant="secondary" size="sm" fullWidth={false} style={{ marginTop: spacing.sm, alignSelf: 'flex-start' }}
        disabled={busy || isZeroAmount(pos.earningsAvailable)} onPress={onWithdraw} />
    </Card>
  );
}

function ActivityRow({ item, pos, sym, decimals }: { item: FiatActivityItem; pos: FiatPosition; sym: string; decimals: number }) {
  const { spacing } = useTheme();
  let title: string;
  let amount: string;
  let pill: { label: string; tone: StatusTone };
  if (item.type === 'deposit') {
    title = 'Added';
    amount = formatCurrency(item.fiat, item.currency, pos.currencies);
    pill = { label: 'Locked', tone: 'success' };
  } else {
    const w = item.w;
    const principal = w.kind === 'principal';
    title = principal ? 'Withdrawal' : 'Earnings withdrawal';
    amount = principal ? formatCurrency(w.fiat, w.currency ?? '', pos.currencies) : `${formatLiquidityAmount(baseUnitsToAmount(w.units, decimals))} ${sym}`;
    pill = WITHDRAWAL_STATUS[w.status] ?? { label: w.status, tone: 'neutral' };
  }
  return (
    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.sm }}>
      <View style={{ flex: 1, gap: 2 }}>
        <Text variant="bodySm" weight="600">{title}</Text>
        <Text variant="caption" color="textMuted">{shortDate(item.at)}</Text>
      </View>
      <View style={{ alignItems: 'flex-end', gap: 4 }}>
        <Text variant="bodySm" weight="600" numeric>{amount}</Text>
        <Pill label={pill.label} tone={pill.tone} />
      </View>
    </View>
  );
}

/** Requests still waiting for the team, with what each will pay. */
export function FiatWaitingCard({ pos, sym, decimals }: { pos: FiatPosition; sym: string; decimals: number }) {
  const { colors, spacing } = useTheme();
  const open = pos.withdrawals.filter((w) => w.status === 'requested');
  if (open.length === 0) return null;
  return (
    <AccentCard padding="md" railColor={colors.warning}>
      <Text variant="bodyMed" weight="700">Waiting to be paid</Text>
      {open.map((w) => (
        <SummaryRow key={w.id} label={w.kind === 'principal' ? 'Withdrawal' : 'Earnings'}
          value={w.kind === 'principal'
            ? formatCurrency(w.fiat, w.currency ?? '', pos.currencies)
            : `${formatLiquidityAmount(baseUnitsToAmount(w.units, decimals))} ${sym}`} />
      ))}
      <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>
        Paid in USDT to your wallet by the TSA team, usually within a day.
      </Text>
    </AccentCard>
  );
}

export function FiatHistory({ pos, sym, decimals }: { pos: FiatPosition; sym: string; decimals: number }) {
  const items = fiatActivity(pos);
  if (items.length === 0) return null;
  return (
    <Card padding="md">
      <Text variant="bodyMed" weight="700">History</Text>
      {items.map((it, i) => (
        <React.Fragment key={it.id}>
          {i > 0 && <Divider />}
          <ActivityRow item={it} pos={pos} sym={sym} decimals={decimals} />
        </React.Fragment>
      ))}
    </Card>
  );
}

/** First visit: what the product is, in three lines. */
export function FiatIntro({ onAdd, disabled }: { onAdd: () => void; disabled: boolean }) {
  const { colors, spacing } = useTheme();
  const points: [string, string][] = [
    ['lock', 'Send USDT and we lock in an amount in your own currency, at today\'s rate.'],
    ['shield', 'You carry no exchange-rate risk: you are owed that amount, however the dollar moves.'],
    ['trendingUp', 'Your USDT still earns its share of the liquidity pool.'],
  ];
  return (
    <Card padding="lg" style={{ gap: spacing.md }}>
      <Text variant="title2">Keep the value of your money</Text>
      {points.map(([icon, text]) => (
        <View key={icon} style={{ flexDirection: 'row', gap: spacing.md, alignItems: 'flex-start' }}>
          <Icon name={icon as 'lock'} size={20} color={colors.accent} />
          <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>{text}</Text>
        </View>
      ))}
      <Text variant="caption" color="textMuted">To leave, request a withdrawal and the team pays you in USDT at that day&apos;s rate.</Text>
      <Button title="Add USDT" variant="primary" disabled={disabled} onPress={onAdd} />
    </Card>
  );
}

export function FiatSkeleton() {
  const { spacing } = useTheme();
  return (
    <View style={{ padding: spacing.lg, gap: spacing.lg }} accessibilityLabel="Loading">
      <Skeleton height={150} radius="lg" />
      <Skeleton height={190} radius="lg" />
      <Skeleton height={110} radius="lg" />
    </View>
  );
}
