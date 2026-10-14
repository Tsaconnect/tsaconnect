// app/wallet/spot-trade.tsx
//
// Spot Trade desk for LiFi-routed pairs (BNB/USDT, ETH/USDT, …) — the same
// form→confirm→sending→result shape as the MCGP desk (app/wallet/mcgp/buy),
// with both legs' balances and an amount box, instead of dropping the user
// onto the generic All Swap pickers. Under the hood it is the SAME swap
// implementation All Swap uses: getLiFiQuote + services/lifiFlow.executeLiFiSwap
// (approve → wait mined → swap).
//
// A pair's two legs each carry their own chain. Usually that is one chain (the
// listing's `chain`); a cross-chain listing also names `quoteChain` — every BTC
// pair does, since Bitcoin carries one asset — and an auto listing
// (`quoteAuto`) puts the quote leg wherever the user already holds it.
//
// Works on every chain LiFi routes, EVM or not. Each leg therefore holds a LiFi
// chain id rather than one of our EVM ChainKeys, and the desk branches on VM
// family exactly where the VM differs: which derived address signs, how a
// balance is read, and whether gas is a thing at all.
import React, { useCallback, useEffect, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, Stack, useLocalSearchParams } from 'expo-router';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  Button, Card, Divider, Spinner, SummaryCard, SummaryRow, Text, Icon,
} from '@/components/ui';
import { PairTradeForm } from '@/components/spot/PairTradeForm';
import AuthorizationModal from '@/components/common/AuthorizationModal';
import { CHAINS } from '@/constants/chains';
import { getActiveWallet, ensureGas, readErc20BalanceOf, getProvider } from '@/services/wallet';
import {
  getLiFiToken, getLiFiTokens, getLiFiQuote, settleSwapFee,
  type LiFiToken, type LiFiQuote,
} from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import type { WalletBalanceWithChain } from '@/services/walletApi';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { getTronAddress, getTronBalances } from '@/services/chains/tronWallet';
import { getBtcAddress, getBtcBalances } from '@/services/chains/btcWallet';
import { fundedQuoteChain } from '@/services/quoteChain';
import {
  resolveSpotChain, resolveSpotLegChain, isSameSpotChain, matchSpotBalance, unsupportedChainMessage,
  singleParam, unhandledFamily, spotLegLookup, isPinnedLegToken,
  type SpotChain, type NonEvmSpotChainFamily,
} from '@/utils/spotChains';

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

// Per-family lookups, typed as total records so that adding a VM family is a
// compile error here rather than a silent fall into Solana's or EVM's rules.
const NON_EVM_BALANCE_READERS: Record<
  NonEvmSpotChainFamily, (address: string) => Promise<WalletBalanceWithChain[]>
> = {
  solana: getSolanaBalances,
  sui: getSuiBalances,
  tron: getTronBalances,
  btc: getBtcBalances,
};

const NON_EVM_ADDRESS_DERIVERS: Record<
  NonEvmSpotChainFamily, (evmAddress: string) => Promise<string | null>
> = {
  solana: getSolanaAddress,
  sui: getSuiAddress,
  tron: getTronAddress,
  btc: getBtcAddress,
};

/** One side of the pair: where it lives, and the address that holds it there. */
interface PairLeg {
  chain: SpotChain;
  address: string;
}

/**
 * Live balances for tokens on ONE chain, in human units, or null where the read
 * failed. EVM reads each token from the chain, so the legs are independent and
 * one failing leaves the other. The non-EVM families have no per-token call to
 * make: every token matches against ONE fetch of the wallet's rows, so that
 * fetch happens once per chain rather than once per leg.
 */
async function readSpotBalances(
  chain: SpotChain, address: string, tokens: LiFiToken[],
): Promise<(string | null)[]> {
  switch (chain.family) {
    case 'evm':
      return Promise.all(tokens.map(async (t) => {
        try {
          const raw = isNativeAddr(t.address)
            ? await getProvider(chain.evmKey).getBalance(address)
            : await readErc20BalanceOf(t.address, address, chain.evmKey);
          return ethers.formatUnits(raw, t.decimals);
        } catch {
          return null;
        }
      }));
    case 'solana':
    case 'sui':
    case 'tron':
    case 'btc': {
      try {
        const rows = await NON_EVM_BALANCE_READERS[chain.family](address);
        return tokens.map((t) => matchSpotBalance(chain.family, rows, t));
      } catch {
        // One fetch backs both legs, so its failure means neither is known.
        return tokens.map(() => null);
      }
    }
    default:
      return unhandledFamily(chain);
  }
}

export default function SpotTrade() {
  const { colors, spacing } = useTheme();
  // Deliberately NOT typed as `{ chain?: string }`: expo-router really can hand
  // back `string[]`, and the generic that promised otherwise is what let a
  // repeated param reach a dependency list. Every param goes through
  // singleParam, which is where the array case (and the render loop it causes)
  // is dealt with — once, before anything can depend on it.
  const params = useLocalSearchParams();
  const side: 'buy' | 'sell' = singleParam(params.side) === 'sell' ? 'sell' : 'buy';
  const baseSymbol = singleParam(params.base).toUpperCase();
  const quoteSymbol = singleParam(params.quote).toUpperCase();
  const chainParam = singleParam(params.chain);
  const quoteChainParam = singleParam(params.quoteChain);
  const quoteAuto = singleParam(params.quoteAuto) === '1';

  // Buying the base means paying the quote token; selling is the reverse.
  const fromSymbol = side === 'buy' ? quoteSymbol : baseSymbol;
  const toSymbol = side === 'buy' ? baseSymbol : quoteSymbol;

  // The EVM wallet id: the key every derived address and signer is looked up by.
  const [walletId, setWalletId] = useState('');
  const [fromLeg, setFromLeg] = useState<PairLeg | null>(null);
  const [toLeg, setToLeg] = useState<PairLeg | null>(null);
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

  const crossChain = !!fromLeg && !!toLeg && !isSameSpotChain(fromLeg.chain, toLeg.chain);

  // Place both legs, resolve each symbol on its own chain, then read the live
  // balances the way each chain's VM allows.
  const setup = useCallback(async () => {
    setSetupLoading(true);
    setSetupError('');
    try {
      const baseChain = resolveSpotLegChain(chainParam);
      if (!baseChain) throw new Error(unsupportedChainMessage(chainParam));

      // Same-chain unless the listing names a quote chain or asks for auto.
      // Auto follows the user's largest balance of the quote asset, falling
      // back to the chain the listing named, else to the base chain when that
      // can hold the quote leg too.
      let quoteKey = chainParam;
      if (quoteAuto) {
        quoteKey = await fundedQuoteChain(
          quoteSymbol, quoteChainParam || (resolveSpotChain(chainParam) ? chainParam : undefined),
        );
      } else if (quoteChainParam) {
        quoteKey = quoteChainParam;
      }
      const quoteChain = resolveSpotLegChain(quoteKey);
      if (!quoteChain) throw new Error(unsupportedChainMessage(quoteKey));
      // Both legs on a single-asset chain is the one pair that cannot exist.
      if (isSameSpotChain(baseChain, quoteChain) && !resolveSpotChain(chainParam)) {
        throw new Error(unsupportedChainMessage(chainParam));
      }

      const evmAddr = await getActiveWallet();
      if (!evmAddr) throw new Error('No wallet found. Set up your wallet first.');
      setWalletId(evmAddr);
      // Every VM family derives its own key from the one mnemonic, so the
      // address on each leg is not always the EVM one.
      const addressOn = async (chain: SpotChain) => {
        const addr = chain.family === 'evm' ? evmAddr : await NON_EVM_ADDRESS_DERIVERS[chain.family](evmAddr);
        if (!addr) {
          throw new Error(`Your wallet has no ${chain.name} address yet. Re-import it with your recovery phrase to trade on ${chain.name}.`);
        }
        return addr;
      };
      const [baseAddr, quoteAddr] = await Promise.all([addressOn(baseChain), addressOn(quoteChain)]);
      const base = { chain: baseChain, address: baseAddr };
      const quoteLeg = { chain: quoteChain, address: quoteAddr };
      const from = side === 'buy' ? quoteLeg : base;
      const to = side === 'buy' ? base : quoteLeg;
      setFromLeg(from);
      setToLeg(to);

      // Resolve each leg with LiFi's single-token lookup (accepts a symbol,
      // tiny payload) — the full per-chain list is multi-MB and flaky on
      // mobile connections. Fall back to the list only if the lookup misses,
      // and never on a chain that pins its tokens (see spotLegLookup).
      const resolve = async (chain: SpotChain, sym: string): Promise<LiFiToken | null> => {
        const lookup = spotLegLookup(chain, sym);
        if (!lookup) return null;
        let token: LiFiToken | null = null;
        try {
          token = await getLiFiToken(chain.lifiChainId, lookup);
        } catch {
          /* fall through to the list */
        }
        if (!token && lookup === sym) {
          try {
            const tokens = await getLiFiTokens(chain.lifiChainId);
            token = tokens.find((t) => t.symbol?.toUpperCase() === sym) ?? null;
          } catch {
            return null;
          }
        }
        return token && isPinnedLegToken(chain, sym, token.address) ? token : null;
      };
      const [fromTok, toTok] = await Promise.all([resolve(from.chain, fromSymbol), resolve(to.chain, toSymbol)]);
      if (!fromTok || !toTok) {
        const [sym, chain] = !fromTok ? [fromSymbol, from.chain] : [toSymbol, to.chain];
        throw new Error(`Couldn’t load ${sym} on ${chain.name}. Check your connection and try again.`);
      }
      setFromToken(fromTok);
      setToToken(toTok);

      const [fb, tb] = isSameSpotChain(from.chain, to.chain)
        ? await readSpotBalances(from.chain, from.address, [fromTok, toTok])
        : (await Promise.all([
          readSpotBalances(from.chain, from.address, [fromTok]),
          readSpotBalances(to.chain, to.address, [toTok]),
        ])).flat();
      setFromBalance(fb == null ? null : Number(fb));
      setFromBalanceStr(fb ?? '0');
      setToBalance(tb == null ? null : Number(tb));
    } catch (e: any) {
      setSetupError(e?.message || 'Could not load this pair');
    } finally {
      setSetupLoading(false);
    }
  }, [chainParam, quoteChainParam, quoteAuto, side, quoteSymbol, fromSymbol, toSymbol]);

  useEffect(() => {
    setup();
  }, [setup]);

  const fetchQuote = useCallback(async () => {
    if (!fromToken || !toToken || !fromLeg || !toLeg) throw new Error('Pair not ready yet');
    const amountWei = ethers.parseUnits(amount, fromToken.decimals).toString();
    const q = await getLiFiQuote({
      fromChain: fromLeg.chain.lifiChainId,
      toChain: toLeg.chain.lifiChainId,
      fromToken: fromToken.address,
      toToken: toToken.address,
      fromAmount: amountWei,
      fromAddress: fromLeg.address,
      // Required whenever the legs sit on different VMs; harmless otherwise.
      toAddress: toLeg.address,
      slippage: SLIPPAGE,
    });
    setQuote(q);
    setQuotedAt(Date.now());
    return q;
  }, [fromToken, toToken, fromLeg, toLeg, amount]);

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
      // Gas is an EVM concern here: every other family pays its fee out of
      // its own native balance, which ensureGas knows nothing about (Tron's
      // shortfall is refused by the backend's preflight with its own note).
      if (fromLeg?.chain.family === 'evm') {
        const { evmKey, name } = fromLeg.chain;
        let gasMsg = '';
        const ok = await ensureGas(evmKey, fromLeg.address, (title, message) => { gasMsg = `${title}: ${message}`; });
        if (!ok) throw new Error(gasMsg || `You need ${CHAINS[evmKey].nativeCurrency.symbol} on ${name} to pay the network fee.`);
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
      if (!fromToken || !fromLeg || !walletId) throw new Error('Pair not ready yet');
      // Refresh a stale quote with the same amount — the user only confirmed
      // an "≈" estimate and slippage still bounds the execution.
      let q = quote;
      if (!q || Date.now() - quotedAt > QUOTE_MAX_AGE_MS) {
        setStage('Refreshing price…');
        q = await fetchQuote();
      }
      if (!toToken || !toLeg) throw new Error('Pair not ready yet');
      // Built BEFORE the trade runs: nothing about reporting the fee may throw
      // once the trade is on-chain, or a completed trade shows as failed.
      // Books against `spot`, not `allswap` — same rate, separate P&L line.
      const feeReport = {
        fromChainId: fromLeg.chain.lifiChainId,
        toChainId: toLeg.chain.lifiChainId,
        fromToken: fromToken.address,
        toToken: toToken.address,
        fromAmount: ethers.parseUnits(amount, fromToken.decimals).toString(),
        toAmount: q.toAmount,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        fromAddress: fromLeg.address,
        toAddress: toLeg.address,
        feature: 'spot' as const,
      };

      const hash = await executeLiFiSwap({
        quote: q,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        // Both are what let the executor route a non-EVM source swap: a Solana
        // or Sui quote's transactionRequest carries no chainId to branch on,
        // and the Sui and Tron paths need the token address to tell the
        // swapped token apart from the coin paying for gas.
        fromTokenAddress: fromToken.address,
        fromChainId: fromLeg.chain.lifiChainId,
        fromAmount: amount,
        // The EVM wallet id, on every family: each executor derives its own
        // signer from it. A derived Solana/Sui address here finds no mnemonic.
        walletAddress: walletId,
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
            {`Your ${toSymbol} will arrive once the swap confirms.`}
            {crossChain ? ' Swaps between networks can take a few minutes — longer for Bitcoin.' : ''}
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
              {crossChain ? (
                <>
                  <SummaryRow label="Pay on" value={fromLeg?.chain.name ?? ''} />
                  <SummaryRow label="Receive on" value={toLeg?.chain.name ?? ''} />
                </>
              ) : (
                <SummaryRow label="Network" value={fromLeg?.chain.name ?? ''} />
              )}
            </SummaryCard>
            <Text variant="bodySm" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              The receive amount is an estimate — the trade executes at the live market rate
              (max slippage {Number(SLIPPAGE) * 100}%).
              {fromLeg?.chain.family === 'evm' || fromLeg?.chain.family === 'tron'
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
  const network = (leg: PairLeg | null) => (crossChain ? leg?.chain.name : undefined);
  return (
    <SafeAreaView edges={['bottom']} style={{ flex: 1, backgroundColor: colors.background }}>
      {header}
      <ScrollView contentContainerStyle={{ padding: spacing.lg }}>
        <PairTradeForm
          balances={[
            { symbol: fromSymbol, amount: fromBalance == null ? null : fmt(fromBalance), network: network(fromLeg) },
            { symbol: toSymbol, amount: toBalance == null ? null : fmt(toBalance), network: network(toLeg) },
          ]}
          onMax={() => fromBalanceStr !== '0' && setAmount(fromBalanceStr)}
          amountLabel={side === 'buy' ? 'Amount to spend' : 'Amount to sell'}
          amount={amount}
          onChangeAmount={setAmount}
          symbol={fromSymbol}
          error={error}
          hint={side === 'buy'
            ? `Spend ${fromSymbol} to buy ${baseSymbol} at the live market rate.`
            : `Sell ${baseSymbol} for ${quoteSymbol} at the live market rate.`}
          onReview={handleReview}
          reviewLoading={loading}
        />
      </ScrollView>
    </SafeAreaView>
  );
}
