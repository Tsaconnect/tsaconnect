// components/profile/NameChangePayForm.tsx
//
// The start of a verified user's name change: what it means (her name becomes
// exactly what her ID shows, the fee, she stays verified meanwhile, the
// limits), what must finish first (money in flight), how she pays the fee —
// spendable MCGP (debited by the server, nothing to send) or USDT/USDC from her
// wallet through the app's one coin picker — and her account password. The
// screen owns what happens next (PIN/biometric, the payment).
import React, { useMemo, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import AssetPicker from '@/components/assets/AssetPicker';
import { Button, Card, Icon, Input, RadioRow, SelectRow, Text } from '@/components/ui';
import { optionFor } from '@/services/nameChangePay';
import {
  parseFeeUsd,
  type McgpMethodQuote, type NameChangeOverview, type StablecoinMethodQuote, type StablecoinOption,
} from '@/services/nameChangeApi';
import { chainLabel, fmtAmount, fmtDate, fmtUsd } from '@/utils/nameChange';

/** A base-units string from the server as an exact integer; null when it isn't one. */
const baseUnits = (s: string | undefined): bigint | null => (s && /^\d+$/.test(s) ? BigInt(s) : null);

/** How she chose to pay. */
export type NameChangePayment =
  | { payMethod: 'free' }
  | { payMethod: 'mcgp_spendable' }
  | { payMethod: 'stablecoin'; option: StablecoinOption };

/** The same payment choice, as a key: a different choice is a different request. */
export const paymentKey = (p: NameChangePayment): string =>
  p.payMethod === 'stablecoin' ? `stablecoin:${p.option.chainId}:${p.option.tokenAddress.toLowerCase()}` : p.payMethod;

export function NameChangePayForm({
  overview, busy, error, walletAddress, password, onChangePassword, onPay,
}: {
  overview: NameChangeOverview;
  busy: boolean;
  error?: string;
  /** The wallet this phone can sign for: the account's linked wallet, a paid fee comes from it. */
  walletAddress: string | null;
  password: string;
  onChangePassword: (v: string) => void;
  onPay: (payment: NameChangePayment) => void;
}) {
  const { colors, spacing } = useTheme();
  const { quote, blockers, eligible, nextAllowedAt, limits } = overview;
  const methods = quote?.methods ?? [];
  const mcgp = methods.find((m): m is McgpMethodQuote => m.method === 'mcgp_spendable');
  const stable = methods.find((m): m is StablecoinMethodQuote => m.method === 'stablecoin');
  const fee = parseFeeUsd(quote?.feeUsd);
  const free = fee === 0;

  // Her spendable MCGP, read by the server for this phone's wallet (the
  // account's linked one, which is debited), compared in exact base units.
  const mcgpNeed = baseUnits(mcgp?.amountBaseUnits);
  const mcgpHave = baseUnits(mcgp?.balanceBaseUnits);
  const mcgpShort = mcgpNeed != null && mcgpHave != null && mcgpHave < mcgpNeed;
  const mcgpUsable = !!mcgp?.available && mcgpNeed != null && !mcgpShort;
  const stableUsable = !!stable?.available && stable.options.length > 0;

  const [method, setMethod] = useState<'mcgp_spendable' | 'stablecoin' | null>(null);
  // MCGP is the default when it covers the fee.
  const chosen = method ?? (mcgpUsable ? 'mcgp_spendable' : stableUsable ? 'stablecoin' : null);
  const [option, setOption] = useState<StablecoinOption | undefined>();
  const [pickerOpen, setPickerOpen] = useState(false);
  const options = useMemo(() => stable?.options ?? [], [stable]);
  // Only what the fee can be paid in, on the networks the server listed.
  const allow = useMemo(
    () => (chainId: number, token?: { symbol?: string; address?: string }) => !!optionFor(options, chainId, token),
    [options],
  );

  const blocked = !quote || blockers.length > 0 || !eligible || Number.isNaN(fee);
  const missing =
    free ? null
    : !walletAddress ? 'Import your wallet on this phone: the fee is paid from it.'
    : chosen === 'mcgp_spendable' ? (mcgpUsable ? null
      : mcgpShort ? 'You don’t have enough spendable MCGP for the fee.' : 'Paying with MCGP isn’t available right now.')
    : chosen === 'stablecoin' ? (option ? null : 'Choose the coin you pay with.')
    : 'No way to pay the fee is available right now.';

  const pay = () => {
    if (free) onPay({ payMethod: 'free' });
    else if (chosen === 'mcgp_spendable' && mcgp) onPay({ payMethod: 'mcgp_spendable' });
    else if (chosen === 'stablecoin' && option) onPay({ payMethod: 'stablecoin', option });
  };

  const bullet = (line: string) => (
    <View key={line} style={{ flexDirection: 'row', alignItems: 'flex-start', gap: spacing.sm }}>
      <Icon name="checkCircle" size={16} color={colors.accent} />
      <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>{line}</Text>
    </View>
  );

  return (
    <View style={{ gap: spacing.md }}>
      <Card style={{ gap: spacing.sm }}>
        <Text variant="bodyMed" weight="700">Change your name</Text>
        {[
          'Your name will be exactly what your ID document shows — you don’t type it. Have the ID ready.',
          ...(quote ? [free ? 'There is no fee for this change.' : `The re-verification fee is ${fmtUsd(quote.feeUsd)}.`] : []),
          'You stay verified meanwhile: payouts keep using your current name until the new one is approved.',
          `You get up to ${limits.maxAttempts} verification attempts within ${limits.ttlDays} days.`,
          `You can change your name up to ${limits.maxPerYear} times a year.`,
        ].map(bullet)}
      </Card>

      {blockers.length > 0 ? (
        <Card style={{ gap: spacing.sm, borderWidth: 1, borderColor: colors.warning }}>
          <Text variant="bodyMed" weight="700">Finish these first</Text>
          <Text variant="bodySm" color="textSecondary">
            Money is still moving under your current name. Once these are done you can change it.
          </Text>
          {blockers.map((b) => (
            <View key={b.code + b.message} style={{ flexDirection: 'row', gap: spacing.sm }}>
              <Icon name="clock" size={16} color={colors.warning} />
              <Text variant="bodySm" style={{ flex: 1 }}>{b.message}</Text>
            </View>
          ))}
        </Card>
      ) : null}

      {!eligible && blockers.length === 0 ? (
        <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
          <Text variant="bodySm">
            {nextAllowedAt
              ? `You’ve reached the limit of name changes for now. You can change your name again on ${fmtDate(nextAllowedAt)}.`
              : 'You can’t change your name right now.'}
          </Text>
        </Card>
      ) : null}

      {blocked ? null : (
        <>
          {free ? null : (
            <View style={{ gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700">Pay the fee with</Text>
              {mcgp ? (
                <RadioRow
                  label="Spendable MCGP"
                  sublabel={mcgp.available && mcgp.amount
                    ? `${fmtAmount(mcgp.amount)} MCGP${mcgp.balance ? ` · you have ${fmtAmount(mcgp.balance)}` : ''}`
                    : 'Not available right now'}
                  leftIcon="wallet"
                  selected={chosen === 'mcgp_spendable'}
                  disabled={!mcgpUsable}
                  onPress={() => setMethod('mcgp_spendable')}
                />
              ) : null}
              {stable ? (
                <RadioRow
                  label="USDT or USDC"
                  sublabel={stableUsable ? 'From your wallet — you sign one transfer' : 'Not available right now'}
                  leftIcon="card"
                  selected={chosen === 'stablecoin'}
                  disabled={!stableUsable}
                  onPress={() => setMethod('stablecoin')}
                />
              ) : null}
              {chosen === 'stablecoin' ? (
                <SelectRow
                  label="Pay with"
                  placeholder="Choose a coin"
                  value={option ? `${fmtAmount(option.amount)} ${option.token} on ${chainLabel(option.chain)}` : undefined}
                  leftIcon="wallet"
                  onPress={() => setPickerOpen(true)}
                />
              ) : null}
            </View>
          )}
          <Input
            label="Account password"
            value={password}
            onChangeText={onChangePassword}
            secureTextEntry
            autoCapitalize="none"
            placeholder="To confirm it's you"
          />
        </>
      )}

      {error ? <Text variant="bodySm" style={{ color: colors.danger }}>{error}</Text> : null}
      {!blocked && missing ? <Text variant="caption" color="textMuted">{missing}</Text> : null}
      {blocked ? null : (
        <Button
          title={free ? 'Continue' : `Pay ${fmtUsd(quote.feeUsd)}`}
          onPress={pay}
          loading={busy}
          disabled={busy || !!missing}
          fullWidth
        />
      )}

      <AssetPicker
        visible={pickerOpen}
        side="from"
        title="Pay the fee with"
        allow={allow}
        onSelect={(t, chainId) => { setOption(optionFor(options, chainId, t)); setPickerOpen(false); }}
        onClose={() => setPickerOpen(false)}
      />
    </View>
  );
}
