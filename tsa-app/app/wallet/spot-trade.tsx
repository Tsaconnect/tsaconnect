// app/wallet/spot-trade.tsx
//
// Spot Trade desk for LiFi-routed pairs (BNB/USDT, ETH/USDT, …) — the same
// form→confirm→sending→result shape as the MCGP desk (app/wallet/mcgp/buy),
// with both legs' balances and an amount box, instead of dropping the user
// onto the generic All Swap pickers. Under the hood it is the SAME swap
// implementation All Swap uses: getLiFiQuote + services/lifiFlow.executeLiFiSwap
// (approve → wait mined → swap). Same-chain only — the pair's chain comes from
// the Spot listing.
//
// Works on every chain LiFi routes a same-chain pair on, EVM or not. The desk
// therefore holds a LiFi chain id rather than one of our EVM ChainKeys, and
// branches on VM family exactly where the VM differs: which derived address
// signs, how a balance is read, and whether gas is a thing at all. Bitcoin is
// excluded upstream in resolveSpotChain — see the note there.
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AmountInput, Button, Card, Divider, PressableScale, Spinner, SummaryCard, SummaryRow, Text, Icon,
} from '@/components/ui';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { CHAINS } from '@/constants/chains';
import { getActiveWallet, ensureGas, readErc20BalanceOf, getProvider } from '@/services/wallet';
import {
  getLiFiToken, getLiFiTokens, getLiFiQuote, settleSwapFee, SOLANA_NATIVE_ADDRESS,
  type LiFiToken, type LiFiQuote,
} from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { resolveSpotChain, isBitcoinSpotChain, type SpotChain } from '@/utils/spotChains';

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

// Bitcoin gets its own wording: it is not a gap in the user's wallet, it is
// that a single-asset chain has no same-chain pair for this desk to trade.
function unsupportedChainMessage(chain: string | undefined): string {
  return isBitcoinSpotChain(chain)
    ? 'Bitcoin pairs have to route across chains, which this desk doesn’t do yet. Use All Swap to trade BTC.'
    : 'This pair’s network is not supported on this device.';
}

/**
 * Live balance of one leg, in human units. EVM reads the chain directly; Solana
 * and Sui have no ERC-20 call to make, so they read the wallet's rows and match
 * the token — by mint for Solana (the native row carries no contract address),
 * by symbol for Sui, whose coin types don't match LiFi's address form.
 */
async function readSpotBalance(chain: SpotChain, address: string, token: LiFiToken): Promise<string> {
  if (chain.family === 'solana') {
    const rows = await getSolanaBalances(address);
    const isNative = token.address === SOLANA_NATIVE_ADDRESS;
    const row = rows.find((b) => (isNative ? !b.contractAddress : b.contractAddress === token.address));
    return row?.balance ?? '0';
  }
  if (chain.family === 'sui') {
    const rows = await getSuiBalances(address);
    const row = rows.find((b) => b.symbol.toUpperCase() === token.symbol.toUpperCase());
    return row?.balance ?? '0';
  }
  if (!chain.evmKey) throw new Error('Unsupported network');
  const raw = isNativeAddr(token.address)
    ? await getProvider(chain.evmKey).getBalance(address)
    : await readErc20BalanceOf(token.address, address, chain.evmKey);
  return ethers.formatUnits(raw, token.decimals);
}

export default function SpotTrade() {
  const { colors, spacing } = useTheme();
  const params = useLocalSearchParams<{ side?: string; base?: string; quote?: string; chain?: string }>();
  const side: 'buy' | 'sell' = params.side === 'sell' ? 'sell' : 'buy';
  const baseSymbol = (params.base || '').toUpperCase();
  const quoteSymbol = (params.quote || '').toUpperCase();
  // Memoised: a fresh object each render would churn every callback below.
  const spotChain = useMemo(() => resolveSpotChain(params.chain), [params.chain]);

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
      if (!spotChain) throw new Error(unsupportedChainMessage(params.chain));
      const evmAddr = await getActiveWallet();
      if (!evmAddr) throw new Error('No wallet found. Set up your wallet first.');
      // Every VM family derives its own key from the one mnemonic, so the
      // address that signs this route is not always the EVM one.
      const addr =
        spotChain.family === 'solana' ? await getSolanaAddress(evmAddr)
        : spotChain.family === 'sui' ? await getSuiAddress(evmAddr)
        : evmAddr;
      if (!addr) {
        throw new Error(`Your wallet has no ${spotChain.name} address yet. Re-import it with your recovery phrase to trade on ${spotChain.name}.`);
      }
      setWallet(addr);

      // Resolve each leg with LiFi's single-token lookup (accepts a symbol,
      // tiny payload) — the full per-chain list is multi-MB and flaky on
      // mobile connections. Fall back to the list only if the lookup misses.
      const resolve = async (sym: string): Promise<LiFiToken | null> => {
        try {
          const t = await getLiFiToken(spotChain.lifiChainId, sym);
          if (t) return t;
        } catch {
          /* fall through to the list */
        }
        try {
          const tokens = await getLiFiTokens(spotChain.lifiChainId);
          return tokens.find((t) => t.symbol?.toUpperCase() === sym) ?? null;
        } catch {
          return null;
        }
      };
      const [from, to] = await Promise.all([resolve(fromSymbol), resolve(toSymbol)]);
      if (!from || !to) {
        throw new Error(`Couldn’t load ${!from ? fromSymbol : toSymbol} on ${spotChain.name}. Check your connection and try again.`);
      }
      setFromToken(from);
      setToToken(to);

      const readBal = (t: LiFiToken) => readSpotBalance(spotChain, addr, t);
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
  }, [spotChain, params.chain, fromSymbol, toSymbol]);

  useEffect(() => {
    setup();
  }, [setup]);

  const fetchQuote = useCallback(async () => {
    if (!fromToken || !toToken || !spotChain || !wallet) throw new Error('Pair not ready yet');
    const amountWei = ethers.parseUnits(amount, fromToken.decimals).toString();
    const q = await getLiFiQuote({
      fromChain: spotChain.lifiChainId,
      toChain: spotChain.lifiChainId,
      fromToken: fromToken.address,
      toToken: toToken.address,
      fromAmount: amountWei,
      fromAddress: wallet,
      slippage: SLIPPAGE,
    });
    setQuote(q);
    setQuotedAt(Date.now());
    return q;
  }, [fromToken, toToken, spotChain, wallet, amount]);

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
      // Gas is an EVM concern here: Solana fees come out of the SOL balance and
      // Sui's out of the SUI one, both of which ensureGas knows nothing about.
      if (spotChain?.family === 'evm' && spotChain.evmKey && wallet) {
        const evmKey = spotChain.evmKey;
        let gasMsg = '';
        const ok = await ensureGas(evmKey, wallet, (title, message) => { gasMsg = `${title}: ${message}`; });
        if (!ok) throw new Error(gasMsg || `You need ${CHAINS[evmKey].nativeCurrency.symbol} on ${spotChain.name} to pay the network fee.`);
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
      if (!toToken || !spotChain) throw new Error('Pair not ready yet');
      // Built BEFORE the trade runs: nothing about reporting the fee may throw
      // once the trade is on-chain, or a completed trade shows as failed.
      // Books against `spot`, not `allswap` — same rate, separate P&L line.
      const feeReport = {
        fromChainId: spotChain.lifiChainId,
        toChainId: spotChain.lifiChainId,
        fromToken: fromToken.address,
        toToken: toToken.address,
        fromAmount: ethers.parseUnits(amount, fromToken.decimals).toString(),
        toAmount: q.toAmount,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        fromAddress: wallet,
        toAddress: wallet,
        feature: 'spot' as const,
      };

      const hash = await executeLiFiSwap({
        quote: q,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        // Both are what let the executor route a non-EVM source swap: a Solana
        // or Sui quote's transactionRequest carries no chainId to branch on,
        // and the Sui path needs the coin type to tell the swapped coin apart
        // from the SUI paying for gas.
        fromTokenAddress: fromToken.address,
        fromChainId: spotChain.lifiChainId,
        fromAmount: amount,
        walletAddress: wallet,
        feeFeature: 'spot',
        onStage: setStage,
      });
      setTxHash(hash);
      setScreen('success');
      settleSwapFee({ ...feeReport, quoteId: hash })
        .catch(e => console.error('[spot] fee settle failed', { txHash: hash, e }));
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
              <SummaryRow label="Network" value={spotChain?.name ?? ''} />
            </SummaryCard>
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              The receive amount is an estimate — the trade executes at the live market rate
              (max slippage {Number(SLIPPAGE) * 100}%).
              {spotChain?.family === 'evm'
                ? ` If ${fromSymbol} isn’t yet approved, you’ll sign an approval first.`
                : ''}
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
