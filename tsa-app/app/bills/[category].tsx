// app/bills/[category].tsx
//
// Shared utility-bill purchase screen, configured per category (airtime / data /
// electricity / cable-TV / betting / exam e-PINs). Flow: choose provider →
// enter customer id → pick amount/plan → validate (electricity/cable) → quote
// (shows the MCGP price and the ₦/MCGP rate — the rate IS the fee, so it is
// shown, not buried) → confirm → poll to a result. Payment is spendable MCGP
// debited server-side by the operator: nothing to sign, no gas, no chain picker.
import React, { useEffect, useMemo, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  Divider,
  Icon,
  PickerSheet,
  SegmentedControl,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  type PickerItem,
} from '@/components/ui';
import { useBillFlow } from '@/hooks/useBillFlow';
import { useMcgpBalances } from '@/hooks/useMcgpBalances';
import {
  getBillProviders,
  getBillPlans,
  type BillCategory,
  type BillPlan,
  type BillProvider,
} from '@/services/billsApi';

interface CategoryConfig {
  title: string;
  customerLabel: string; // e.g. "Phone number", "Meter number"
  customerPlaceholder: string;
  // amount source: user enters a free amount, or picks a plan (fixed price)
  amountSource: 'free' | 'plan';
  usesPhone: boolean; // send phoneNumber (the customer input doubles as it)
  usesMeterType: boolean; // electricity prepaid/postpaid
  validates: boolean; // pre-pay account-holder lookup
}

const CONFIG: Record<BillCategory, CategoryConfig> = {
  airtime: {
    title: 'Airtime',
    customerLabel: 'Phone number',
    customerPlaceholder: '080XXXXXXXX',
    amountSource: 'free',
    usesPhone: true,
    usesMeterType: false,
    validates: false,
  },
  data: {
    title: 'Data',
    customerLabel: 'Phone number',
    customerPlaceholder: '080XXXXXXXX',
    amountSource: 'plan',
    usesPhone: true,
    usesMeterType: false,
    validates: false,
  },
  electricity: {
    title: 'Electricity',
    customerLabel: 'Meter number',
    customerPlaceholder: 'Meter / account number',
    amountSource: 'free',
    usesPhone: false,
    usesMeterType: true,
    validates: true,
  },
  cabletv: {
    title: 'Cable TV',
    customerLabel: 'Smartcard number',
    customerPlaceholder: 'Smartcard / IUC number',
    amountSource: 'plan',
    usesPhone: false,
    usesMeterType: false,
    validates: true,
  },
  betting: {
    title: 'Betting',
    customerLabel: 'Account ID',
    customerPlaceholder: 'Betting account / user ID',
    amountSource: 'free',
    usesPhone: false,
    usesMeterType: false,
    validates: false,
  },
  epin: {
    title: 'Exam PINs',
    customerLabel: 'Phone number',
    customerPlaceholder: '080XXXXXXXX',
    amountSource: 'plan',
    usesPhone: true,
    usesMeterType: false,
    validates: false,
  },
};

const formatMcgp = (base: string): string => {
  try {
    const n = Number(ethers.formatUnits(base || '0', 18));
    return n.toLocaleString(undefined, { maximumFractionDigits: 2 });
  } catch {
    return '0';
  }
};

const rateNgnPerMcgp = (rateKobo: string): string => {
  const kobo = Number(rateKobo || '0');
  return (kobo / 100).toLocaleString(undefined, { maximumFractionDigits: 2 });
};

export default function BillPurchase() {
  const { colors, spacing } = useTheme();
  const params = useLocalSearchParams<{ category: string }>();
  const category =
    (params.category as BillCategory) in CONFIG ? (params.category as BillCategory) : 'airtime';
  const cfg = CONFIG[category];
  const { state, validate, quote, pay, reset } = useBillFlow();
  const { mcgpPremium, loading: balanceLoading, refresh: refreshBalance } = useMcgpBalances();

  const [providers, setProviders] = useState<BillProvider[]>([]);
  const [plans, setPlans] = useState<BillPlan[]>([]);
  const [provider, setProvider] = useState<BillProvider | null>(null);
  const [plan, setPlan] = useState<BillPlan | null>(null);
  const [customerId, setCustomerId] = useState('');
  const [naira, setNaira] = useState('');
  const [meterType, setMeterType] = useState<'prepaid' | 'postpaid'>('prepaid');
  const [providerSheet, setProviderSheet] = useState(false);
  const [planSheet, setPlanSheet] = useState(false);
  const [loadingCatalog, setLoadingCatalog] = useState(false);
  const [localErr, setLocalErr] = useState('');

  // Operator catalog for the category.
  useEffect(() => {
    setLoadingCatalog(true);
    getBillProviders(category).then((r) => {
      if (r.success && Array.isArray(r.data)) {
        setProviders(r.data);
        if (r.data.length === 1) setProvider(r.data[0]);
      }
      setLoadingCatalog(false);
    });
  }, [category]);

  // Plans for plan-priced categories once a provider is chosen.
  useEffect(() => {
    if (cfg.amountSource !== 'plan' || !provider) return;
    setLoadingCatalog(true);
    getBillPlans(category, provider.code).then((r) => {
      if (r.success && Array.isArray(r.data)) setPlans(r.data);
      setLoadingCatalog(false);
    });
  }, [category, cfg.amountSource, provider]);

  const effectiveNgn = cfg.amountSource === 'plan' ? (plan?.amount ?? 0) : Number(naira) || 0;

  const providerItems: PickerItem[] = providers.map((p) => ({ id: p.code, label: p.name }));
  const planItems: PickerItem[] = plans.map((p) => ({
    id: p.code,
    label: p.name,
    sublabel: `₦${p.amount.toLocaleString()}`,
  }));

  const busy = ['validating', 'quoting', 'paying', 'vending'].includes(state.status);

  const mcgpNeeded = useMemo(
    () => (state.quote ? Number(ethers.formatUnits(state.quote.mcgpAmount || '0', 18)) : 0),
    [state.quote]
  );
  const insufficient = state.status === 'quoted' && mcgpNeeded > mcgpPremium;

  const canQuote = !!provider && effectiveNgn > 0 && customerId.trim().length > 0 && !busy;

  const onValidate = async () => {
    setLocalErr('');
    if (!provider) return setLocalErr('Choose a provider');
    if (!customerId.trim()) return setLocalErr(`Enter the ${cfg.customerLabel.toLowerCase()}`);
    await validate(category, provider.code, customerId.trim());
  };

  const onGetQuote = async () => {
    setLocalErr('');
    if (!provider) return setLocalErr('Choose a provider');
    if (effectiveNgn <= 0) return setLocalErr('Enter an amount');
    if (!customerId.trim()) return setLocalErr(`Enter the ${cfg.customerLabel.toLowerCase()}`);
    if (provider.minAmount && cfg.amountSource === 'free' && effectiveNgn < provider.minAmount)
      return setLocalErr(`Minimum is ₦${provider.minAmount.toLocaleString()}`);
    if (provider.maxAmount && cfg.amountSource === 'free' && effectiveNgn > provider.maxAmount)
      return setLocalErr(`Maximum is ₦${provider.maxAmount.toLocaleString()}`);
    await quote({
      category,
      provider: provider.code,
      customerId: customerId.trim(),
      customerName: state.customerName,
      phoneNumber: cfg.usesPhone ? customerId.trim() : undefined,
      planRef: plan?.code,
      meterType: cfg.usesMeterType ? meterType : undefined,
      ngnAmount: cfg.amountSource === 'free' ? effectiveNgn : undefined,
    });
  };

  const onConfirmPay = async () => {
    if (!state.order) return;
    await pay(state.order.id);
    refreshBalance();
  };

  // ── Terminal result states ─────────────────────────────────────────────────
  if (
    state.status === 'delivered' ||
    state.status === 'refunded' ||
    state.status === 'failed' ||
    state.status === 'error'
  ) {
    const ok = state.status === 'delivered';
    const refunded = state.status === 'refunded';
    const paidNgn = state.order?.ngnAmount ?? effectiveNgn;
    return (
      <SafeAreaView
        style={{
          flex: 1,
          justifyContent: 'center',
          alignItems: 'center',
          padding: spacing.xl,
          backgroundColor: colors.background,
        }}
      >
        <Icon
          name={ok ? 'checkCircle' : 'closeCircle'}
          size={64}
          color={ok ? colors.success : colors.danger}
        />
        <Text
          variant="title3"
          weight="700"
          style={{ marginTop: spacing.lg, color: colors.textPrimary, textAlign: 'center' }}
        >
          {ok ? `${cfg.title} delivered` : refunded ? 'Payment refunded' : 'Payment failed'}
        </Text>
        <Text
          variant="bodySm"
          style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}
        >
          {ok
            ? `Your ${cfg.title.toLowerCase()} purchase of ₦${paidNgn.toLocaleString()} was successful.`
            : refunded
              ? 'We couldn’t complete the bill, so your MCGP has been returned to your balance.'
              : state.error || 'The bill could not be completed. Your MCGP will be refunded shortly.'}
        </Text>
        {ok && state.order?.deliveryPayload ? (
          <Card style={{ marginTop: spacing.lg, alignSelf: 'stretch' }}>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              {category === 'epin' ? 'Your PIN' : 'Token'}
            </Text>
            <Text
              variant="body"
              weight="700"
              selectable
              style={{ color: colors.textPrimary, marginTop: spacing.xs }}
            >
              {state.order.deliveryPayload}
            </Text>
          </Card>
        ) : null}
        <View style={{ height: spacing.xl }} />
        <Button title="Done" onPress={() => router.replace('/bills' as any)} fullWidth />
        <View style={{ height: spacing.sm }} />
        <Button
          title="Buy another"
          variant="ghost"
          onPress={() => {
            reset();
            refreshBalance();
          }}
          fullWidth
        />
      </SafeAreaView>
    );
  }

  // ── Busy (quoting/paying/vending) ──────────────────────────────────────────
  if (state.status === 'quoting' || state.status === 'paying' || state.status === 'vending') {
    const label =
      state.status === 'quoting'
        ? 'Pricing your bill…'
        : state.status === 'paying'
          ? 'Paying with your MCGP balance…'
          : 'Delivering your bill…';
    return (
      <SafeAreaView
        style={{
          flex: 1,
          justifyContent: 'center',
          alignItems: 'center',
          backgroundColor: colors.background,
          gap: spacing.md,
          padding: spacing.xl,
        }}
      >
        <Spinner />
        <Text variant="body" style={{ color: colors.textSecondary }}>
          {label}
        </Text>
        {state.status === 'vending' && (
          <Text variant="bodySm" style={{ color: colors.textMuted, textAlign: 'center' }}>
            This can take a moment. You can close this screen — the result will appear in your bill
            history.
          </Text>
        )}
      </SafeAreaView>
    );
  }

  // ── Quote confirmation ─────────────────────────────────────────────────────
  if (state.status === 'quoted' && state.quote) {
    const q = state.quote;
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
          <SummaryCard>
            <SummaryRow label="Bill" value={`${cfg.title} · ${provider?.name ?? ''}`} />
            {plan ? <SummaryRow label="Plan" value={plan.name} /> : null}
            <SummaryRow label={cfg.customerLabel} value={customerId} />
            {state.customerName ? <SummaryRow label="Name" value={state.customerName} /> : null}
            <Divider spacing="xs" />
            <SummaryRow label="Amount" value={`₦${q.ngnAmount.toLocaleString()}`} />
            <SummaryRow label="You pay" value={`${formatMcgp(q.mcgpAmount)} MCGP`} />
            <SummaryRow label="Rate" value={`₦${rateNgnPerMcgp(q.rateKobo)} / MCGP`} />
          </SummaryCard>

          <Card>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
              <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                Spendable MCGP balance
              </Text>
              <Text variant="bodySm" weight="700" style={{ color: colors.textPrimary }}>
                {balanceLoading ? '…' : mcgpPremium.toLocaleString(undefined, { maximumFractionDigits: 2 })}
              </Text>
            </View>
          </Card>

          {insufficient && (
            <Text variant="bodySm" style={{ color: colors.danger }}>
              You don’t have enough spendable MCGP for this bill.
            </Text>
          )}

          <Button
            title="Confirm & Pay"
            onPress={onConfirmPay}
            disabled={insufficient || balanceLoading}
            fullWidth
          />
          <Button title="Back" variant="ghost" onPress={reset} fullWidth />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Form ───────────────────────────────────────────────────────────────────
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}>
        <Card>
          <Text
            variant="bodySm"
            weight="600"
            style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
          >
            Provider
          </Text>
          <SelectRow
            value={provider?.name}
            placeholder={loadingCatalog ? 'Loading…' : 'Select provider'}
            onPress={() => setProviderSheet(true)}
          />

          {cfg.usesMeterType && (
            <View style={{ marginTop: spacing.md }}>
              <Text
                variant="bodySm"
                weight="600"
                style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
              >
                Meter type
              </Text>
              <SegmentedControl
                segments={[
                  { key: 'prepaid', label: 'Prepaid' },
                  { key: 'postpaid', label: 'Postpaid' },
                ]}
                value={meterType}
                onChange={(v) => setMeterType(v as 'prepaid' | 'postpaid')}
              />
            </View>
          )}

          <View style={{ marginTop: spacing.md }}>
            <Text
              variant="bodySm"
              weight="600"
              style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
            >
              {cfg.customerLabel}
            </Text>
            <AmountInput
              value={customerId}
              onChangeText={(v) => setCustomerId(v.replace(/[^0-9]/g, ''))}
              placeholder={cfg.customerPlaceholder}
              symbol={undefined}
            />
            {cfg.validates && (
              <View
                style={{
                  marginTop: spacing.xs,
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                }}
              >
                <Button
                  title="Verify"
                  size="sm"
                  variant="secondary"
                  onPress={onValidate}
                  loading={state.status === 'validating'}
                />
                {state.customerName ? (
                  <Text variant="bodySm" weight="600" style={{ color: colors.success }}>
                    {state.customerName}
                  </Text>
                ) : null}
              </View>
            )}
          </View>

          {cfg.amountSource === 'plan' ? (
            <View style={{ marginTop: spacing.md }}>
              <Text
                variant="bodySm"
                weight="600"
                style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
              >
                Plan
              </Text>
              <SelectRow
                value={plan ? `${plan.name} · ₦${plan.amount.toLocaleString()}` : undefined}
                placeholder={
                  !provider
                    ? 'Choose a provider first'
                    : loadingCatalog
                      ? 'Loading plans…'
                      : 'Select a plan'
                }
                onPress={() => provider && setPlanSheet(true)}
              />
            </View>
          ) : (
            <View style={{ marginTop: spacing.md }}>
              <Text
                variant="bodySm"
                weight="600"
                style={{ color: colors.textSecondary, marginBottom: spacing.xs }}
              >
                Amount
              </Text>
              <AmountInput value={naira} onChangeText={setNaira} symbol="₦" placeholder="0" />
              {provider?.minAmount ? (
                <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
                  ₦{provider.minAmount.toLocaleString()} – ₦
                  {(provider.maxAmount || 0).toLocaleString()}
                </Text>
              ) : null}
            </View>
          )}
        </Card>

        {effectiveNgn > 0 && (
          <SummaryCard>
            <SummaryRow label="You pay" value={`₦${effectiveNgn.toLocaleString()} in MCGP`} />
            <Divider spacing="xs" />
            <SummaryRow
              label="Spendable MCGP"
              value={
                balanceLoading
                  ? '…'
                  : mcgpPremium.toLocaleString(undefined, { maximumFractionDigits: 2 })
              }
            />
          </SummaryCard>
        )}

        {(localErr || state.error) && (
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {localErr || state.error}
          </Text>
        )}

        <Button title="Continue" onPress={onGetQuote} disabled={!canQuote} fullWidth />
      </ScrollView>

      <PickerSheet
        visible={providerSheet}
        onClose={() => setProviderSheet(false)}
        title="Select provider"
        items={providerItems}
        selectedId={provider?.code}
        loading={loadingCatalog}
        onSelect={(item) => {
          setProvider(providers.find((p) => p.code === item.id) || null);
          setPlan(null);
          setPlans([]);
          setProviderSheet(false);
        }}
      />
      <PickerSheet
        visible={planSheet}
        onClose={() => setPlanSheet(false)}
        title="Select a plan"
        items={planItems}
        selectedId={plan?.code}
        loading={loadingCatalog}
        onSelect={(item) => {
          setPlan(plans.find((p) => p.code === item.id) || null);
          setPlanSheet(false);
        }}
      />
    </SafeAreaView>
  );
}
