// app/wallet/spot-trade.tsx
//
// Spot Trade desk for LiFi-routed pairs (BNB/USDT, ETH/USDT, …) — the same
// form→confirm→sending→result shape as the MCGP desk (app/wallet/mcgp/buy),
// with both legs' balances and an amount box, instead of dropping the user
// onto the generic All Swap pickers. Under the hood it is the SAME swap
// implementation All Swap uses: getLiFiQuote + services/lifiFlow.executeLiFiSwap
// (approve → wait mined → swap). Same-chain only — the pair's chain comes from
// the Spot listing.
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, PressableScale, Spinner, SummaryCard, SummaryRow, Text, Icon,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { CHAINS, type ChainKey } from '@/constants/chains';
import { getActiveWallet, ensureGas, readErc20BalanceOf, getProvider } from '@/services/wallet';
import { getLiFiToken, getLiFiTokens, getLiFiQuote, settleSwapFee, type LiFiToken, type LiFiQuote } from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

const SLIPPAGE = '0.005';
// Re-quote before executing when the confirm screen sat longer than this —
// LiFi quotes go stale in ~30s and the user only ever saw an "≈" estimate.
const QUOTE_MAX_AGE_MS = 45_000;

const isNativeAddr = (address: string) => {
  const a = (address || '').toLowerCase();
  return a === '0x0000000000000000000000000000000000000000' || a === '0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
};

function fmt(n: number, maxFrac = 6): string {
  if (!isFinite(n) || n === 0) return '0';
  return n.toLocaleString('en-US', { maximumFractionDigits: maxFrac });
}

export default function SpotTrade() {
  const { colors, spacing } = useTheme();
  const params = useLocalSearchParams<{ side?: string; base?: string; quote?: string; chain?: string }>();
  const side: 'buy' | 'sell' = params.side === 'sell' ? 'sell' : 'buy';
  const baseSymbol = (params.base || '').toUpperCase();
  const quoteSymbol = (params.quote || '').toUpperCase();
  const chainKey = params.chain as ChainKey | undefined;
  const chain = chainKey ? CHAINS[chainKey] : undefined;

  // Buying the base means paying the quote token; selling is the reverse.
  const fromSymbol = side === 'buy' ? quoteSymbol : baseSymbol;
  const toSymbol = side === 'buy' ? baseSymbol : quoteSymbol;

  const [wallet, setWallet] = useState('');
  const [fromToken, setFromToken] = useState<LiFiToken | null>(null);
  const [toToken, setToToken] = useState<LiFiToken | null>(null);
  const [fromBalance, setFromBalance] = useState<number | null>(null);
  const [toBalance, setToBalance] = useState<number | null>(null);
  const [fromBalanceStr, setFromBalanceStr] = useState('0'); // exact, for Max
  const [setupError, setSetupError] = useState('');
  const [setupLoading, setSetupLoading] = useState(true);

  const [screen, setScreen] = useState<Screen>('form');
  const [amount, setAmount] = useState('');
  const [error, setError] = useState('');
  const [loading, setLoading] = useState(false);
  const [showAuth, setShowAuth] = useState(false);
  const [quote, setQuote] = useState<LiFiQuote | null>(null);
  const [quotedAt, setQuotedAt] = useState(0);
  const [stage, setStage] = useState('');
  const [txHash, setTxHash] = useState('');

  // Resolve the pair's symbols against the chain's LiFi token list, then read
  // both legs' live balances (native via getBalance, ERC-20 via balanceOf).
  const setup = useCallback(async () => {
    setSetupLoading(true);
    setSetupError('');
    try {
      if (!chain || !chainKey) throw new Error('This pair’s network is not supported on this device.');
      const addr = await getActiveWallet();
      if (!addr) throw new Error('No wallet found. Set up your wallet first.');
      setWallet(addr);

      // Resolve each leg with LiFi's single-token lookup (accepts a symbol,
      // tiny payload) — the full per-chain list is multi-MB and flaky on
      // mobile connections. Fall back to the list only if the lookup misses.
      const resolve = async (sym: string): Promise<LiFiToken | null> => {
        try {
          const t = await getLiFiToken(chain.chainId, sym);
          if (t) return t;
        } catch {
          /* fall through to the list */
        }
        try {
          const tokens = await getLiFiTokens(chain.chainId);
          return tokens.find((t) => t.symbol?.toUpperCase() === sym) ?? null;
        } catch {
          return null;
        }
      };
      const [from, to] = await Promise.all([resolve(fromSymbol), resolve(toSymbol)]);
      if (!from || !to) {
        throw new Error(`Couldn’t load ${!from ? fromSymbol : toSymbol} on ${chain.name}. Check your connection and try again.`);
      }
      setFromToken(from);
      setToToken(to);

      const readBal = async (t: LiFiToken) => {
        const raw = isNativeAddr(t.address)
          ? await getProvider(chainKey).getBalance(addr)
          : await readErc20BalanceOf(t.address, addr, chainKey);
        return ethers.formatUnits(raw, t.decimals);
      };
      const [fb, tb] = await Promise.all([
        readBal(from).catch(() => null),
        readBal(to).catch(() => null),
      ]);
      setFromBalance(fb == null ? null : Number(fb));
      setFromBalanceStr(fb ?? '0');
      setToBalance(tb == null ? null : Number(tb));
    } catch (e: any) {
      setSetupError(e?.message || 'Could not load this pair');
    } finally {
      setSetupLoading(false);
    }
  }, [chain, chainKey, fromSymbol, toSymbol]);

  useEffect(() => {
    setup();
  }, [setup]);

  const fetchQuote = useCallback(async () => {
    if (!fromToken || !toToken || !chain || !wallet) throw new Error('Pair not ready yet');
    const amountWei = ethers.parseUnits(amount, fromToken.decimals).toString();
    const q = await getLiFiQuote({
      fromChain: chain.chainId,
      toChain: chain.chainId,
      fromToken: fromToken.address,
      toToken: toToken.address,
      fromAmount: amountWei,
      fromAddress: wallet,
      slippage: SLIPPAGE,
    });
    setQuote(q);
    setQuotedAt(Date.now());
    return q;
  }, [fromToken, toToken, chain, wallet, amount]);

  const handleReview = async () => {
    setError('');
    if (!amount || Number(amount) <= 0) {
      setError('Enter an amount');
      return;
    }
    if (fromBalance != null && Number(amount) > fromBalance) {
      setError(`Insufficient ${fromSymbol} balance`);
      return;
    }
    try {
      setLoading(true);
      if (chainKey && wallet) {
        let gasMsg = '';
        const ok = await ensureGas(chainKey, wallet, (title, message) => { gasMsg = `${title}: ${message}`; });
        if (!ok) throw new Error(gasMsg || `You need ${chain?.nativeCurrency.symbol ?? 'gas'} on ${chain?.name ?? 'this network'} to pay the network fee.`);
      }
      await fetchQuote();
      setScreen('confirm');
    } catch (e: any) {
      setError(e?.message || 'Could not get a price for this pair');
    } finally {
      setLoading(false);
    }
  };

  const handleTrade = async () => {
    setShowAuth(false);
    setScreen('sending');
    setError('');
    try {
      if (!fromToken || !wallet) throw new Error('Pair not ready yet');
      // Refresh a stale quote with the same amount — the user only confirmed
      // an "≈" estimate and slippage still bounds the execution.
      let q = quote;
      if (!q || Date.now() - quotedAt > QUOTE_MAX_AGE_MS) {
        setStage('Refreshing price…');
        q = await fetchQuote();
      }
      const hash = await executeLiFiSwap({
        quote: q,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        fromAmount: amount,
        walletAddress: wallet,
        onStage: setStage,
      });
      setTxHash(hash);
      setScreen('success');
      // Books against `spot`, not `allswap`: same rate, separate line in the
      // P&L. Fire-and-forget — the trade is already on-chain, so a settle
      // failure must not show up as a failed trade.
      settleSwapFee({
        quoteId: hash,
        fromChainId: chain!.chainId,
        toChainId: chain!.chainId,
        fromToken: fromToken.address,
        toToken: toToken!.address,
        fromAmount: ethers.parseUnits(amount, fromToken.decimals).toString(),
        toAmount: q.toAmount,
        fromSymbol: fromToken.symbol,
        feature: 'spot',
      }).catch(e => console.error('[spot] fee settle failed', { txHash: hash, e }));
    } catch (e: any) {
      setError(e?.message || 'Trade failed');
      setScreen('failure');
    }
  };

  const receiveEstimate = quote && toToken
    ? (() => {
        try {
          return fmt(Number(ethers.formatUnits(quote.toAmount, toToken.decimals)));
        } catch {
          return null;
        }
      })()
    : null;

  const title = `${side === 'buy' ? 'Buy' : 'Sell'} ${baseSymbol || 'token'}`;
  const header = <Stack.Screen options={{ title }} />;

  if (setupLoading) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        {header}
        <Spinner size="large" label={`Loading ${baseSymbol}/${quoteSymbol}…`} />
      </SafeAreaView>
    );
  }

  if (setupError) {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        {header}
        <Icon name="closeCircle" size={64} color={colors.danger} />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary, textAlign: 'center' }}>
          {setupError}
        </Text>
        <View style={{ height: spacing.xl }} />
        <Button title="Try again" onPress={setup} />
        <View style={{ marginTop: spacing.md }}>
          <Button title="Go back" variant="secondary" onPress={() => router.back()} />
        </View>
      </SafeAreaView>
    );
  }

  if (screen === 'sending') {
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', backgroundColor: colors.background }}>
        {header}
        <Spinner size="large" />
        <Text variant="body" style={{ marginTop: spacing.lg, color: colors.textSecondary }}>
          {stage || 'Submitting trade…'}
        </Text>
      </SafeAreaView>
    );
  }

  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <SafeAreaView style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing.xl, backgroundColor: colors.background }}>
        {header}
        <Icon name={ok ? 'checkCircle' : 'closeCircle'} size={64} color={ok ? colors.success : colors.danger} />
        <Text variant="title1" weight="700" style={{ marginTop: spacing.lg, color: colors.textPrimary }}>
          {ok ? 'Trade submitted' : 'Trade failed'}
        </Text>
        {ok ? (
          <Text variant="body" style={{ marginTop: spacing.sm, color: colors.textSecondary, textAlign: 'center' }}>
            {side === 'buy' ? `Your ${baseSymbol} will arrive once the swap confirms.` : `Your ${quoteSymbol} will arrive once the swap confirms.`}
            {txHash ? `\nTx: ${txHash.slice(0, 10)}…${txHash.slice(-8)}` : ''}
          </Text>
        ) : (
          <Text variant="bodySm" style={{ marginTop: spacing.sm, color: colors.danger, textAlign: 'center' }}>
            {error}
          </Text>
        )}
        <View style={{ height: spacing.xl }} />
        <Button title="Done" onPress={() => router.back()} />
        {!ok ? (
          <View style={{ marginTop: spacing.md }}>
            <Button title="Try again" variant="secondary" onPress={() => setScreen('form')} />
          </View>
        ) : null}
      </SafeAreaView>
    );
  }

  if (screen === 'confirm') {
    return (
      <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
        {header}
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
          <Card>
            <SummaryCard>
              <SummaryRow label="You pay" value={`${amount} ${fromSymbol}`} />
              <Divider spacing="xs" />
              <SummaryRow label="You receive" value={receiveEstimate ? `≈ ${receiveEstimate} ${toSymbol}` : '—'} />
              <SummaryRow label="Network" value={chain?.name ?? ''} />
            </SummaryCard>
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              The receive amount is an estimate — the trade executes at the live market rate
              (max slippage {Number(SLIPPAGE) * 100}%). If {fromSymbol} isn&apos;t yet approved, you&apos;ll
              sign an approval first.
            </Text>
          </Card>
          <Button title={side === 'buy' ? `Buy ${baseSymbol}` : `Sell ${baseSymbol}`} onPress={() => setShowAuth(true)} />
          <Button title="Back" variant="ghost" onPress={() => setScreen('form')} />
        </ScrollView>
        <AuthorizationModal
          visible={showAuth}
          title={`Authorize ${side}`}
          description={`${side === 'buy' ? 'Buy' : 'Sell'} ${baseSymbol} — pay ${amount} ${fromSymbol}`}
          onAuthorized={handleTrade}
          onCancel={() => setShowAuth(false)}
        />
      </SafeAreaView>
    );
  }

  // form
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      {header}
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
          <Text variant="bodySm" color="textSecondary">
            Balance: {fromBalance == null ? '—' : fmt(fromBalance)} {fromSymbol} · {toBalance == null ? '—' : fmt(toBalance)} {toSymbol}
          </Text>
          <PressableScale onPress={() => fromBalanceStr !== '0' && setAmount(fromBalanceStr)} accessibilityLabel="Use max">
            <Text variant="bodySm" weight="700" color="accent">Max</Text>
          </PressableScale>
        </View>
        <AmountInput
          label={side === 'buy' ? 'Amount to spend' : 'Amount to sell'}
          value={amount}
          onChangeText={setAmount}
          symbol={fromSymbol}
          symbolPosition="suffix"
          error={error || undefined}
        />
        <Text variant="bodySm" color="textMuted">
          {side === 'buy'
            ? `Spend ${fromSymbol} to buy ${baseSymbol} at the live market rate.`
            : `Sell ${baseSymbol} for ${quoteSymbol} at the live market rate.`}
        </Text>
        <Button title="Review" onPress={handleReview} loading={loading} />
      </ScrollView>
    </SafeAreaView>
  );
}
