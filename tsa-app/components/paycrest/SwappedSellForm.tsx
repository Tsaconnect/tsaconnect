// components/paycrest/SwappedSellForm.tsx
//
// The swapped sale's form (design §5): a coin Paycrest cannot take, sold by
// swapping it to USDC on Base in her own wallet first. She types how much of
// the coin, sees one "~₦" figure, and taps Sell once — useSwappedSell does
// the rest. Where the naira goes is the screen's, shared with the direct sale,
// and slotted in as `payout`.

import React, { useCallback, useMemo, useState, type ReactNode } from 'react';
import { View } from 'react-native';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import { AmountInput, Button, Card, Spinner, Text } from '@/components/ui';
import type { SellStage } from '@/hooks/useSwappedSell';
import { useSellSwapQuote } from '@/hooks/useSellSwapQuote';
import { useWalletHoldings } from '@/hooks/useWalletHoldings';
import { isNativeLiFiToken } from '@/services/assetCatalog';
import { nativeGasReserve } from '@/services/wallet';
import { spendableNative } from '@/services/gasReserve';
import { getChainByChainId } from '@/constants/chains';
import type { LiFiToken } from '@/services/lifi';
import { fmtTokenAmount } from '@/services/paycrestDeposit';

/** A coin Paycrest cannot take, sold by swapping it to USDC on Base first. */
export type SwapPick = { token: LiFiToken; chainId: number; sourceAddress: string };

/** What a running swapped sale is doing, by stage — none when it is not running. */
export const SWAPPED_STAGE_LABELS: Partial<Record<SellStage, string>> = {
  swapping: 'Swapping your coins…',
  waiting: 'Waiting for your swap to arrive — this can take a few minutes…',
  ordering: 'Opening your payout…',
  sending: 'Sending for payout…',
};


/** A float balance as a decimal string, rounded DOWN so MAX never asks for
 *  more than she holds. */
function floorBalance(balance: number, decimals: number): string {
  const places = Math.min(decimals, 8);
  const scale = 10 ** places;
  return (Math.floor(balance * scale) / scale).toFixed(places);
}

const toBig = (v?: string) => {
  try {
    return v ? BigInt(v) : undefined;
  } catch {
    return undefined;
  }
};

type Props = {
  pick: SwapPick;
  walletAddress: string | null | undefined;
  currency: string;
  /** The fiat symbol her estimate is shown in. */
  symbol: string;
  /** The payout destination is chosen and verified. */
  payoutReady: boolean;
  /** A swapped sale is running right now. */
  busy: boolean;
  /** Where the naira goes — the screen's own fields. */
  payout: ReactNode;
  /** The running sale's progress or failure, above the Sell button. */
  status: ReactNode;
  onSell: (amount: string) => void;
};

export function SwappedSellForm({ pick, walletAddress, currency, symbol, payoutReady, busy, payout, status, onSell }: Props) {
  const { colors, spacing } = useTheme();
  // The amount belongs to the pick it was typed for: a new pick starts empty.
  const [entry, setEntry] = useState({ pick, amount: '' });
  const amount = entry.pick === pick ? entry.amount : '';
  const setAmount = useCallback((next: string) => setEntry({ pick, amount: next }), [pick]);

  const { quote, quoting, error } = useSellSwapQuote({
    token: pick.token,
    chainId: pick.chainId,
    sourceAddress: pick.sourceAddress,
    amount,
    walletAddress,
    currency,
  });
  const { holdings } = useWalletHoldings(true);

  const holding = useMemo(() => {
    const native = isNativeLiFiToken({ chainId: pick.chainId, address: pick.token.address });
    return holdings.find(
      (h) =>
        h.chainId === pick.chainId &&
        (native ? h.native : h.address.toLowerCase() === pick.token.address.toLowerCase()),
    );
  }, [pick, holdings]);

  const overBalance = !!holding && Number(amount) > 0 && Number(amount) > holding.balance;

  // MAX: her whole balance, less All Swap's gas reserve when the coin being
  // sold is the one that pays for the swap.
  const onMax = useCallback(async () => {
    if (!holding) return;
    const { token, chainId } = pick;
    let units = ethers.parseUnits(floorBalance(holding.balance, token.decimals), token.decimals);
    const chain = getChainByChainId(chainId);
    if (chain && holding.native) {
      const reserve = await nativeGasReserve(chain.key, {
        gasUnits: toBig(quote?.quote.transactionRequest?.gasLimit),
        gasCostWei: toBig(quote?.quote.gasCostWei),
      });
      if (reserve != null) units = spendableNative(units, reserve);
    }
    setAmount(ethers.formatUnits(units, token.decimals));
  }, [pick, holding, quote, setAmount]);

  const ready = !!walletAddress && payoutReady && !!quote && !quoting && !overBalance && !busy;

  return (
    <>
      <AmountInput
        label="You sell"
        value={amount}
        onChangeText={setAmount}
        symbol={pick.token.symbol}
        symbolPosition="suffix"
        onMax={holding ? onMax : undefined}
        helper={holding ? `Balance: ${fmtTokenAmount(holding.balance)} ${pick.token.symbol}` : undefined}
      />
      {quoting && (
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
          <Spinner size="small" />
          <Text variant="caption" style={{ color: colors.textMuted }}>
            Checking live rates…
          </Text>
        </View>
      )}
      {quote && !quoting && (
        <Card>
          <Text variant="bodySm" style={{ color: colors.textSecondary }}>
            You receive about
          </Text>
          <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
            {`~${symbol}${Number(quote.fiatEstimate).toLocaleString(undefined, { maximumFractionDigits: 0 })}`}
          </Text>
        </Card>
      )}
      {error && !quoting && (
        <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
          <Text variant="bodySm" style={{ color: colors.warning }}>
            {error}
          </Text>
        </Card>
      )}
      {overBalance && (
        <Text variant="caption" style={{ color: colors.warning }}>
          That is more {pick.token.symbol} than you have.
        </Text>
      )}

      {payout}
      {status}

      <Button title="Sell" onPress={() => onSell(amount.trim())} loading={busy} disabled={!ready} fullWidth />
    </>
  );
}

/** A running swapped sale's progress, or why it stopped. Nothing when idle. */
export function SwappedSellStatus({ stage, error }: { stage: SellStage; error?: string }) {
  const { colors, spacing } = useTheme();
  const label = SWAPPED_STAGE_LABELS[stage];
  return (
    <>
      {label && (
        <Card style={{ gap: spacing.sm }}>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
            <Spinner size="small" />
            <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, flex: 1 }}>
              {label}
            </Text>
          </View>
          <Text variant="caption" style={{ color: colors.textMuted }}>
            Keep the app open. If it closes, come back here and tap Finish your sale.
          </Text>
        </Card>
      )}
      {stage === 'failed' && error && (
        <Card style={{ borderWidth: 1, borderColor: colors.danger }}>
          <Text variant="bodySm" style={{ color: colors.danger }}>
            {error}
          </Text>
        </Card>
      )}
    </>
  );
}
