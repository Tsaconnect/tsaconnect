// screens/all-swap.tsx
//
// Cross-chain "All Swap" — pick any token on any chain, get a LiFi quote, confirm,
// then sign approval + swap. Restyled onto the TSA Signature design system (theme
// tokens + @/components/ui primitives). All quote/fee/decimal math, the LiFi quote
// lifecycle (debounce + expiry countdown + refresh), the two-step approve→swap
// signing flow, live on-chain balance reads, navigation, and the TokenPickerModal
// wiring are unchanged — this is a visual/structural restyle only.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  View, ScrollView, ActivityIndicator, KeyboardAvoidingView, Platform,
  Modal, Pressable, Image, Linking, StyleSheet, TextInput as RNTextInput,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import Animated, {
  useSharedValue, useAnimatedStyle, withSpring, withTiming,
  withDelay, withSequence,
} from 'react-native-reanimated';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { getChainByChainId, CHAINS, NONEVM_CHAINS, txExplorerUrl } from '@/constants/chains';
import { getWalletBalances, normalizeWalletBalances, type WalletBalanceWithChain } from '@/services/walletApi';
import { getActiveWallet, readErc20BalanceOf, getProvider } from '@/services/wallet';
import { getSolanaAddress, getSolanaBalances } from '@/services/chains/solanaWallet';
import { ethers } from 'ethers';
import {
  getLiFiTokens, getLiFiAllChainTokens, getLiFiQuote,
  isSolanaLiFiChain, SOLANA_NATIVE_ADDRESS,
  type LiFiToken, type LiFiQuote,
} from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import TokenPickerModal from '@/components/swap/TokenPickerModal';
import { useTheme } from '@/theme';
import {
  Button,
  Card,
  Icon,
  IconButton,
  PressableScale,
  Text,
  Toggle,
} from '@/components/ui';

const FEE_PCT = 3;
const DEFAULT_FROM_CHAIN_ID = 56;
const DEFAULT_TO_CHAIN_ID = 1;

type QuoteStatus = 'idle' | 'quoting' | 'quoted' | 'no_route' | 'error' | 'expired';
type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

function TokenIcon({ uri, symbol, size = 32 }: { uri?: string; symbol?: string; size?: number }) {
  const { colors } = useTheme();
  if (uri) return <Image source={{ uri }} style={{ width: size, height: size, borderRadius: size / 2 }} />;
  if (!symbol) return null;
  return (
    <View style={{ width: size, height: size, borderRadius: size / 2, backgroundColor: colors.surfaceAlt, alignItems: 'center', justifyContent: 'center' }}>
      <Text style={{ fontSize: size * 0.38 }} weight="700" color="textSecondary">{symbol[0]}</Text>
    </View>
  );
}

export default function AllSwapScreen() {
  const { colors, spacing, radius } = useTheme();
  const [fromToken, setFromToken] = useState<LiFiToken | null>(null);
  const [fromChainId, setFromChainId] = useState(DEFAULT_FROM_CHAIN_ID);
  const [toToken, setToToken] = useState<LiFiToken | null>(null);
  const [toChainId, setToChainId] = useState(DEFAULT_TO_CHAIN_ID);
  const [fromAmount, setFromAmount] = useState('');
  const [quote, setQuote] = useState<LiFiQuote | null>(null);
  const [quoteStatus, setQuoteStatus] = useState<QuoteStatus>('idle');
  const [quoteError, setQuoteError] = useState('');
  const [screen, setScreen] = useState<Screen>('form');
  const [pickerMode, setPickerMode] = useState<'from' | 'to' | null>(null);
  const [allTokens, setAllTokens] = useState<LiFiToken[]>([]);
  const [toAllTokens, setToAllTokens] = useState<LiFiToken[]>([]);
  const [tokensLoading, setTokensLoading] = useState(false);
  const [toTokensLoading, setToTokensLoading] = useState(false);
  const [allChainsTokens, setAllChainsTokens] = useState<LiFiToken[]>([]);
  const [allChainsLoading, setAllChainsLoading] = useState(false);
  const [allChainsTotal, setAllChainsTotal] = useState(0);
  const [allChainsQuery, setAllChainsQuery] = useState('');
  const allChainsLoadedQueryRef = useRef<string | null>(null);
  const ALL_CHAINS_PAGE_SIZE = 100;
  const [balances, setBalances] = useState<WalletBalanceWithChain[]>([]);
  const [signingStatus, setSigningStatus] = useState('');
  const [txHash, setTxHash] = useState('');
  const [error, setError] = useState('');
  const [slippageAuto, setSlippageAuto] = useState(true);
  const [slippage, setSlippage] = useState('0.005');
  const [slippageSheetVisible, setSlippageSheetVisible] = useState(false);
  const [walletAddress, setWalletAddress] = useState('');
  const [quoteSecondsLeft, setQuoteSecondsLeft] = useState(0);
  const [refreshKey, setRefreshKey] = useState(0);
  const [walletLoading, setWalletLoading] = useState(true);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const countdownRef = useRef<ReturnType<typeof setInterval> | null>(null);

  // Derived Solana address for the active wallet — the from/to address on
  // Solana-side routes. null until derived (or when no mnemonic exists).
  const [solanaAddress, setSolanaAddress] = useState<string | null>(null);

  useEffect(() => {
    getActiveWallet()
      .then(addr => { if (addr) setWalletAddress(addr); })
      .finally(() => setWalletLoading(false));
  }, []);

  useEffect(() => {
    if (!walletAddress) return;
    let cancelled = false;
    getSolanaAddress(walletAddress)
      .then(addr => { if (!cancelled) setSolanaAddress(addr); })
      .catch(() => {});
    return () => { cancelled = true; };
  }, [walletAddress]);

  // Lazy-load all-chain tokens paginated. Initial page is fetched the first
  // time the picker is opened in "All" mode (see `loadAllChainsPage`); we don't
  // pre-fetch at mount because the LiFi cross-chain blob is several MB and
  // blocks the home screen if shipped eagerly.
  const loadAllChainsPage = useCallback(async (opts: { reset?: boolean; q?: string } = {}) => {
    const q = (opts.q ?? allChainsQuery).trim();
    const reset = !!opts.reset;
    if (allChainsLoading) return;
    // When the query changes we always reset the cursor.
    if (allChainsLoadedQueryRef.current !== q) {
      allChainsLoadedQueryRef.current = q;
    }
    setAllChainsLoading(true);
    try {
      const offset = reset ? 0 : allChainsTokens.length;
      const page = await getLiFiAllChainTokens({ offset, limit: ALL_CHAINS_PAGE_SIZE, q: q || undefined });
      setAllChainsTotal(page.total);
      setAllChainsTokens(prev => (reset ? page.tokens : [...prev, ...page.tokens]));
    } catch {
      // Swallow: picker will fall back to per-chain `allTokens`.
    } finally {
      setAllChainsLoading(false);
    }
  }, [allChainsLoading, allChainsTokens.length, allChainsQuery]);

  // First-page load triggered when the picker opens for the first time.
  useEffect(() => {
    if (pickerMode === null) return;
    if (allChainsLoadedQueryRef.current !== null) return;
    void loadAllChainsPage({ reset: true, q: '' });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [pickerMode]);

  // Reset+refetch when the user types into the picker's search (debounced
  // inside the picker — see the `onSearchChange` prop).
  const handleAllChainsSearchChange = useCallback((q: string) => {
    setAllChainsQuery(q);
    setAllChainsTokens([]);
    allChainsLoadedQueryRef.current = null;
    void loadAllChainsPage({ reset: true, q });
  }, [loadAllChainsPage]);

  const handleAllChainsLoadMore = useCallback(() => {
    if (allChainsLoading) return;
    if (allChainsTokens.length >= allChainsTotal) return;
    void loadAllChainsPage();
  }, [allChainsLoading, allChainsTokens.length, allChainsTotal, loadAllChainsPage]);

  useFocusEffect(useCallback(() => {
    loadBalances();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fromChainId, solanaAddress]));

  useEffect(() => {
    let active = true;
    setTokensLoading(true);
    getLiFiTokens(fromChainId)
      .then(tokens => { if (active) setAllTokens(tokens); })
      .catch(() => {})
      .finally(() => { if (active) setTokensLoading(false); });
    return () => { active = false; };
  }, [fromChainId]);

  useEffect(() => {
    let active = true;
    setToTokensLoading(true);
    getLiFiTokens(toChainId)
      .then(tokens => { if (active) setToAllTokens(tokens); })
      .catch(() => {})
      .finally(() => { if (active) setToTokensLoading(false); });
    return () => { active = false; };
  }, [toChainId]);

  const loadBalances = async () => {
    try {
      // /wallet/balances is EVM-only — it 400s on the Solana pseudo chain-id
      // and would leave the previous EVM chain's rows in place, which the
      // picker's "your tokens" section then matches BY SYMBOL against SPL
      // tokens. Read the SPL rows instead (or clear while the address derives).
      if (isSolanaLiFiChain(fromChainId)) {
        if (!solanaAddress) { setBalances([]); return; }
        setBalances(await getSolanaBalances(solanaAddress));
        return;
      }
      const res = await getWalletBalances(fromChainId);
      if (res.success && res.data) {
        setBalances(normalizeWalletBalances(res.data, 'bsc'));
      }
    } catch {}
  };

  // Live on-chain balance for the currently-selected `fromToken`. The BE's
  // /wallet/balances endpoint only knows about supported tokens, so user-
  // imported customs (CREPE, etc.) come back as 0 and the swap form blocks
  // input. Re-read directly via RPC whenever the picker changes, so any
  // token the wallet actually holds — supported or imported — gets an
  // accurate balance.
  const [liveFromBalance, setLiveFromBalance] = useState<string | null>(null);
  useEffect(() => {
    if (!fromToken || !walletAddress) {
      setLiveFromBalance(null);
      return;
    }
    let cancelled = false;
    (async () => {
      try {
        if (isSolanaLiFiChain(fromChainId)) {
          if (!solanaAddress) return;
          const rows = await getSolanaBalances(solanaAddress);
          if (cancelled) return;
          const isNativeSol = fromToken.address === SOLANA_NATIVE_ADDRESS;
          const row = rows.find(b =>
            isNativeSol ? !b.contractAddress : b.contractAddress === fromToken.address
          );
          setLiveFromBalance(row?.balance ?? '0');
          return;
        }
        const chain = getChainByChainId(fromChainId);
        if (!chain) return;
        const addr = fromToken.address.toLowerCase();
        const isNative =
          addr === '0x0000000000000000000000000000000000000000' ||
          addr === '0xeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee';
        const raw = isNative
          ? await getProvider(chain.key).getBalance(walletAddress)
          : await readErc20BalanceOf(fromToken.address, walletAddress, chain.key);
        if (cancelled) return;
        setLiveFromBalance(ethers.formatUnits(raw, fromToken.decimals));
      } catch {
        if (!cancelled) setLiveFromBalance(null);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [fromToken, fromChainId, walletAddress, solanaAddress, refreshKey]);

  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    if (!fromToken || !toToken || !fromAmount || isNaN(Number(fromAmount)) || Number(fromAmount) <= 0 || !walletAddress) {
      setQuote(null); setQuoteStatus('idle'); setQuoteError('');
      return;
    }
    // Solana-side routes quote against the derived Solana address; skip until
    // it's available (derivation is near-instant once the wallet loads).
    const fromSolana = isSolanaLiFiChain(fromChainId);
    const toSolana = isSolanaLiFiChain(toChainId);
    if ((fromSolana || toSolana) && !solanaAddress) {
      setQuote(null); setQuoteStatus('idle'); setQuoteError('');
      return;
    }
    setQuoteStatus('quoting');
    timer.current = setTimeout(async () => {
      try {
        const amountWei = ethers.parseUnits(fromAmount, fromToken.decimals).toString();
        // Cross-VM routes must name the destination-chain recipient explicitly;
        // both sides derive from the same mnemonic, so no user input needed.
        const q = await getLiFiQuote({
          fromChain: fromChainId,
          toChain: toChainId,
          fromToken: fromToken.address,
          toToken: toToken.address,
          fromAmount: amountWei,
          fromAddress: fromSolana ? solanaAddress! : walletAddress,
          ...(fromSolana !== toSolana
            ? { toAddress: toSolana ? solanaAddress! : walletAddress }
            : {}),
          slippage: slippageAuto ? '0.005' : slippage,
        });
        setQuote(q); setQuoteStatus('quoted'); setQuoteError('');
        // Start expiry countdown — use validUntil from LiFi if present, else 30s
        const expiresAt = q.validUntil
          ? q.validUntil * 1000
          : Date.now() + 30_000;
        if (countdownRef.current) clearInterval(countdownRef.current);
        countdownRef.current = setInterval(() => {
          const left = Math.max(0, Math.round((expiresAt - Date.now()) / 1000));
          setQuoteSecondsLeft(left);
          if (left === 0) {
            clearInterval(countdownRef.current!);
            setQuoteStatus('expired');
          }
        }, 1000);
        setQuoteSecondsLeft(Math.round((expiresAt - Date.now()) / 1000));
      } catch (e: any) {
        const msg: string = e?.message ?? '';
        if (msg.toLowerCase().includes('no available quotes') || msg.toLowerCase().includes('not supported')) {
          setQuoteStatus('no_route');
        } else {
          setQuoteStatus('error'); setQuoteError(msg);
        }
        setQuote(null);
      }
    }, 500);
    return () => {
      if (timer.current) clearTimeout(timer.current);
      if (countdownRef.current) clearInterval(countdownRef.current);
    };
  }, [fromToken, toToken, fromAmount, fromChainId, toChainId, walletAddress, solanaAddress, slippageAuto, slippage, refreshKey]);

  const swapDirection = () => {
    const [nf, nfc, nt, ntc] = [toToken, toChainId, fromToken, fromChainId];
    setFromToken(nf); setFromChainId(nfc);
    setToToken(nt); setToChainId(ntc);
    setFromAmount(''); setQuote(null); setQuoteStatus('idle');
  };

  // `balances` holds EVM rows from /wallet/balances — never let a Solana
  // from-token fall through to them by symbol (EVM USDC ≠ SPL USDC).
  const fromBalance =
    liveFromBalance ??
    (isSolanaLiFiChain(fromChainId)
      ? '0'
      : balances.find(b => b.symbol === fromToken?.symbol)?.balance ?? '0');

  const validate = () => {
    if (!fromToken) return 'Select a token to send';
    if (!toToken) return 'Select a token to receive';
    if (!fromAmount || Number(fromAmount) <= 0) return 'Enter an amount';
    try {
      const amountUnits = ethers.parseUnits(fromAmount, fromToken.decimals);
      const balanceUnits = ethers.parseUnits(fromBalance || '0', fromToken.decimals);
      if (amountUnits > balanceUnits) return `Insufficient ${fromToken.symbol} balance`;
    } catch {
      return 'Enter a valid amount';
    }
    if (quoteStatus === 'expired') return 'Quote expired — refresh first';
    if (!quote) return 'Waiting for price...';
    return null;
  };

  // Approve→wait→swap execution lives in services/lifiFlow (shared with the
  // Spot Trade desk); this handler only owns the screen-state wiring.
  const handleSwap = async () => {
    if (!quote || !fromToken || !walletAddress) return;
    setScreen('signing'); setError('');
    try {
      const swapHash = await executeLiFiSwap({
        quote,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        fromAmount,
        walletAddress,
        fromChainId,
        onStage: setSigningStatus,
      });
      setTxHash(swapHash); setScreen('success');
    } catch (e: any) {
      console.error('[swap] handleSwap failed', e);
      setError(e?.message || 'Swap failed'); setScreen('failure');
    }
  };

  const outputAmount = quote && toToken
    ? (() => {
        try {
          const raw = ethers.formatUnits(quote.toAmount, toToken.decimals);
          const n = Number(raw);
          if (isNaN(n) || n === 0) return '0';
          return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
        } catch { return '0'; }
      })()
    : null;

  const refreshQuote = () => {
    if (countdownRef.current) clearInterval(countdownRef.current);
    setQuote(null); setQuoteStatus('quoting'); setQuoteSecondsLeft(0);
    setRefreshKey(k => k + 1);
  };

  const fromAmountUSD = fromToken?.priceUSD && fromAmount && Number(fromAmount) > 0
    ? Number(fromAmount) * Number(fromToken.priceUSD)
    : null;
  const toAmountUSD = quote?.toAmountUSD ? Number(quote.toAmountUSD) : null;
  const priceImpactPct = fromAmountUSD && toAmountUSD && fromAmountUSD > 0
    ? ((toAmountUSD - fromAmountUSD) / fromAmountUSD) * 100
    : null;

  const chainName = (chainId: number) => {
    if (isSolanaLiFiChain(chainId)) return NONEVM_CHAINS.solana.shortName;
    const entry = Object.values(CHAINS).find(c => c.chainId === chainId);
    return entry?.shortName ?? String(chainId);
  };

  const formattedBalance = fromBalance
    ? (() => {
        const n = Number(fromBalance);
        if (isNaN(n) || n === 0) return '0';
        return n.toLocaleString('en-US', { maximumFractionDigits: 6, minimumFractionDigits: 0 });
      })()
    : '0';

  // ── Signing ──────────────────────────────────────────────────
  if (screen === 'signing') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
        <View
          style={{
            width: 80, height: 80, borderRadius: radius.pill,
            borderWidth: 2, borderColor: colors.border,
            alignItems: 'center', justifyContent: 'center', marginBottom: spacing['2xl'],
          }}
        >
          <ActivityIndicator size="large" color={colors.accent} />
        </View>
        <Text variant="title1" weight="700">Processing Swap</Text>
        <Text variant="bodySm" weight="600" color="accent" style={{ marginTop: spacing.sm }}>{signingStatus}</Text>
        <Text variant="caption" color="textMuted" style={{ marginTop: spacing.xs }}>Please keep the app open</Text>
      </View>
    </SafeAreaView>
  );

  // ── Success / Failure ─────────────────────────────────────────
  if (screen === 'success') return (
    <SwapSuccessScreen
      fromToken={fromToken}
      toToken={toToken}
      fromAmount={fromAmount}
      outputAmount={outputAmount}
      txHash={txHash}
      chainId={quote?.transactionRequest.chainId ?? fromChainId}
      onDone={() => router.back()}
    />
  );

  if (screen === 'failure') return (
    <SwapFailureScreen
      error={error}
      onRetry={() => setScreen('form')}
    />
  );

  // ── Confirm ───────────────────────────────────────────────────
  if (screen === 'confirm') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ flexGrow: 1, padding: spacing.xl, paddingBottom: spacing['4xl'] }}>
        <PressableScale
          onPress={() => setScreen('form')}
          style={{ flexDirection: 'row', alignItems: 'center', gap: 2, marginBottom: spacing.lg }}
        >
          <Icon name="chevronLeft" size={20} color={colors.accent} />
          <Text variant="bodySm" weight="600" color="accent">Back</Text>
        </PressableScale>
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>Confirm Swap</Text>

        {/* From → To summary */}
        <Card padding="lg">
          <View style={{ paddingVertical: spacing.md }}>
            <Text variant="overline" color="textMuted">YOU PAY</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginTop: spacing.sm }}>
              <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={40} />
              <View style={{ flex: 1 }}>
                <Text variant="title2" weight="700">{fromAmount} {fromToken?.symbol}</Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>{fromToken?.name} · {chainName(fromChainId)}</Text>
              </View>
            </View>
          </View>

          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginVertical: spacing.xs }}>
            <View style={{ flex: 1, height: 1, backgroundColor: colors.hairline }} />
            <View
              style={{
                width: 28, height: 28, borderRadius: 14, backgroundColor: colors.accent,
                alignItems: 'center', justifyContent: 'center',
              }}
            >
              <Icon name="arrowDown" size={16} color={colors.onAccent} />
            </View>
            <View style={{ flex: 1, height: 1, backgroundColor: colors.hairline }} />
          </View>

          <View style={{ paddingVertical: spacing.md }}>
            <Text variant="overline" color="textMuted">YOU RECEIVE</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginTop: spacing.sm }}>
              <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={40} />
              <View style={{ flex: 1 }}>
                <Text variant="title2" weight="700" color="accent">{outputAmount} {toToken?.symbol}</Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>{toToken?.name} · {chainName(toChainId)}</Text>
              </View>
            </View>
          </View>
        </Card>

        {/* Details */}
        <Card padding="lg" style={{ marginTop: spacing.md }}>
          <DetailRow label="Route" value={quote?.tool ?? '—'} />
          <DetailRow label="Est. time" value={quote ? `~${Math.ceil(quote.estimatedDuration / 60)} min` : '—'} />
          <DetailRow label="Gas cost" value={`$${quote?.gasCostUSD ?? '0'}`} />
          <DetailRow label="Fee" value={`${FEE_PCT}%`} highlight />
          <DetailRow label="Slippage" value={slippageAuto ? '0.5% (auto)' : `${(Number(slippage) * 100).toFixed(1)}%`} last />
        </Card>

        {quote?.approvalAddress && (
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.md, marginBottom: spacing.xs }}>
            <Icon name="info" size={14} color={colors.textMuted} />
            <Text variant="caption" color="textMuted" style={{ flex: 1 }}>Two transactions required: token approval + swap</Text>
          </View>
        )}

        <Button
          title="Confirm Swap"
          leftIcon="swapVertical"
          onPress={handleSwap}
          size="lg"
          style={{ marginTop: spacing.xl }}
        />
        <Button
          title="Cancel"
          variant="ghost"
          onPress={() => setScreen('form')}
          style={{ marginTop: spacing.sm }}
        />
      </ScrollView>
    </SafeAreaView>
  );

  // ── Wallet not connected ─────────────────────────────────────
  if (!walletLoading && !walletAddress) return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
        <View
          style={{
            width: 80, height: 80, borderRadius: radius.pill,
            backgroundColor: colors.surfaceAlt,
            alignItems: 'center', justifyContent: 'center',
          }}
        >
          <Icon name="wallet" size={40} color={colors.textMuted} />
        </View>
        <Text variant="title1" weight="700" style={{ marginTop: spacing.lg }}>No Wallet Connected</Text>
        <Text variant="bodySm" color="textSecondary" align="center" style={{ marginTop: spacing.sm, paddingHorizontal: spacing['3xl'] }}>
          You need a wallet to swap tokens. Set one up in the Wallet tab first.
        </Text>
        <Button
          title="Go to Wallet"
          leftIcon="wallet"
          onPress={() => router.push('/tokenization')}
          fullWidth={false}
          size="lg"
          style={{ marginTop: spacing['3xl'], paddingHorizontal: spacing['3xl'] }}
        />
      </View>
    </SafeAreaView>
  );

  // ── Form ──────────────────────────────────────────────────────
  const validationError = validate();
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView contentContainerStyle={{ flexGrow: 1, padding: spacing.xl, paddingBottom: spacing['4xl'] }} keyboardShouldPersistTaps="handled">
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: spacing.xl }}>
            <View>
              <Text variant="title1" weight="700">All Swap</Text>
              <Text variant="bodySm" color="textMuted" style={{ marginTop: 2 }}>Any token · Any chain</Text>
            </View>
            <IconButton
              icon="filter"
              size={20}
              color={colors.textMuted}
              accessibilityLabel="Slippage settings"
              onPress={() => setSlippageSheetVisible(true)}
            />
          </View>

          <Card padding="lg">
            {/* From */}
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>YOU PAY</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.xs }}>
              <PressableScale
                style={{
                  flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                  backgroundColor: colors.surfaceAlt, paddingHorizontal: spacing.sm, paddingVertical: spacing.sm,
                  borderRadius: radius.xl, maxWidth: '50%',
                }}
                onPress={() => setPickerMode('from')}
              >
                <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={24} />
                <View>
                  <Text variant="bodySm" weight="700">{fromToken?.symbol ?? 'Select'}</Text>
                  {fromToken && <Text variant="caption" color="textMuted" style={{ marginTop: 1 }}>{chainName(fromChainId)}</Text>}
                </View>
                <Icon name="chevronDown" size={13} color={colors.textMuted} />
              </PressableScale>
              <RNTextInput
                style={{ flex: 1, fontSize: 28, fontWeight: '700', color: colors.textPrimary, textAlign: 'right', padding: 0 }}
                placeholder="0.00"
                placeholderTextColor={colors.textDisabled}
                value={fromAmount}
                onChangeText={t => { setFromAmount(t); setError(''); }}
                keyboardType="decimal-pad"
              />
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.lg }}>
              {fromAmountUSD != null && (
                <Text variant="caption" color="textMuted">≈ ${fromAmountUSD.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 })}</Text>
              )}
              <PressableScale onPress={() => fromBalance !== '0' && setFromAmount(fromBalance)}>
                <Text variant="caption" weight="500" color="accent">Balance: {formattedBalance} {fromToken?.symbol}</Text>
              </PressableScale>
            </View>

            {/* Swap direction button */}
            <View style={{ alignItems: 'center', marginVertical: spacing.xs }}>
              <PressableScale
                style={{
                  width: 38, height: 38, borderRadius: 19, backgroundColor: colors.textPrimary,
                  justifyContent: 'center', alignItems: 'center',
                }}
                onPress={swapDirection}
              >
                <Icon name="swapVertical" size={18} color={colors.background} />
              </PressableScale>
            </View>

            {/* To */}
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>YOU RECEIVE</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.xs }}>
              <PressableScale
                style={{
                  flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                  backgroundColor: colors.surfaceAlt, paddingHorizontal: spacing.sm, paddingVertical: spacing.sm,
                  borderRadius: radius.xl, maxWidth: '50%',
                }}
                onPress={() => setPickerMode('to')}
              >
                <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={24} />
                <View>
                  <Text variant="bodySm" weight="700">{toToken?.symbol ?? 'Select'}</Text>
                  {toToken && <Text variant="caption" color="textMuted" style={{ marginTop: 1 }}>{chainName(toChainId)}</Text>}
                </View>
                <Icon name="chevronDown" size={13} color={colors.textMuted} />
              </PressableScale>
              {quoteStatus === 'quoting'
                ? <ActivityIndicator size="small" color={colors.accent} style={{ flex: 1 }} />
                : <View style={{ flex: 1, alignItems: 'flex-end' }}>
                    <Text style={{ fontSize: 28 }} weight="700" color="accent">{outputAmount ?? '—'}</Text>
                    {toAmountUSD != null && (
                      <Text variant="caption" color="textMuted">≈ ${toAmountUSD.toLocaleString('en-US', { maximumFractionDigits: 2, minimumFractionDigits: 2 })}</Text>
                    )}
                  </View>
              }
            </View>
          </Card>

          {/* Quote info / errors */}
          {(quoteStatus === 'quoted' || quoteStatus === 'expired') && quote && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: colors.accentMuted, borderWidth: 1, borderColor: colors.accent,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="bolt" size={13} color={colors.accent} />
              <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                {quote.tool} · ~{Math.ceil(quote.estimatedDuration / 60)} min · {FEE_PCT}% fee · ${quote.gasCostUSD} gas
              </Text>
              {quoteStatus === 'expired' ? (
                <PressableScale onPress={refreshQuote} style={{ flexDirection: 'row', alignItems: 'center', gap: 3 }}>
                  <Icon name="refresh" size={13} color={colors.accent} />
                  <Text variant="caption" weight="700" color="accent">Refresh</Text>
                </PressableScale>
              ) : (
                <View style={{ backgroundColor: colors.surfaceAlt, borderRadius: radius.sm, paddingHorizontal: 6, paddingVertical: 2 }}>
                  <Text variant="caption" weight="700" color="textSecondary">{quoteSecondsLeft}s</Text>
                </View>
              )}
            </View>
          )}
          {/* Price impact warning */}
          {priceImpactPct != null && priceImpactPct < -5 && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: priceImpactPct < -15 ? colors.dangerBg : colors.warningBg,
                borderWidth: 1, borderColor: priceImpactPct < -15 ? colors.danger : colors.warning,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="warning" size={13} color={priceImpactPct < -15 ? colors.danger : colors.warning} />
              <Text variant="caption" style={{ flex: 1, color: priceImpactPct < -15 ? colors.danger : colors.warning }}>
                {priceImpactPct < -15 ? 'High' : 'Moderate'} price impact ({Math.abs(priceImpactPct).toFixed(1)}%) — check liquidity before swapping
              </Text>
            </View>
          )}
          {quoteStatus === 'no_route' && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: colors.dangerBg, borderWidth: 1, borderColor: colors.danger,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="warning" size={13} color={colors.danger} />
              <Text variant="caption" style={{ flex: 1, color: colors.danger }}>No route available for this pair. Try a different token or amount.</Text>
            </View>
          )}
          {quoteStatus === 'error' && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: colors.dangerBg, borderWidth: 1, borderColor: colors.danger,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="dispute" size={13} color={colors.danger} />
              <Text variant="caption" style={{ flex: 1, color: colors.danger }}>{quoteError || 'Swap pricing unavailable. Try again shortly.'}</Text>
            </View>
          )}
          {quoteStatus === 'expired' && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: colors.warningBg, borderWidth: 1, borderColor: colors.warning,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="clock" size={13} color={colors.warning} />
              <Text variant="caption" style={{ flex: 1, color: colors.warning }}>Quote expired — tap Refresh for a new price</Text>
            </View>
          )}

          <Button
            title="Review Swap"
            onPress={() => { const e = validate(); if (e) { setError(e); return; } setScreen('confirm'); }}
            disabled={!!validationError}
            size="lg"
            style={{ marginTop: spacing.xl }}
          />
        </ScrollView>

        <TokenPickerModal
          visible={pickerMode !== null}
          mode={pickerMode ?? 'from'}
          chainId={pickerMode === 'from' ? fromChainId : toChainId}
          userBalances={balances}
          allTokens={pickerMode === 'from' ? allTokens : toAllTokens}
          allChainsTokens={allChainsTokens}
          allChainsLoading={allChainsLoading}
          allChainsHasMore={allChainsTokens.length < allChainsTotal}
          onAllChainsLoadMore={handleAllChainsLoadMore}
          onAllChainsSearchChange={handleAllChainsSearchChange}
          tokensLoading={pickerMode === 'from' ? tokensLoading : toTokensLoading}
          onChainChange={(newChainId) => {
            if (pickerMode === 'from') setFromChainId(newChainId);
            else setToChainId(newChainId);
          }}
          onSelect={(token, chainId) => {
            if (pickerMode === 'from') { setFromToken(token); setFromChainId(chainId); }
            else { setToToken(token); setToChainId(chainId); }
            setPickerMode(null);
            setFromAmount(''); setQuote(null); setQuoteStatus('idle');
          }}
          onClose={() => setPickerMode(null)}
        />

        <Modal visible={slippageSheetVisible} transparent animationType="slide">
          <Pressable style={{ flex: 1, backgroundColor: colors.overlay, justifyContent: 'flex-end' }} onPress={() => setSlippageSheetVisible(false)}>
            <Pressable
              style={{
                backgroundColor: colors.surface, borderTopLeftRadius: radius.xl, borderTopRightRadius: radius.xl,
                padding: spacing['2xl'], paddingTop: spacing.lg, gap: spacing.xl,
              }}
              onPress={() => {}}
            >
              <View style={{ width: 36, height: 4, borderRadius: 2, backgroundColor: colors.border, alignSelf: 'center', marginBottom: spacing.sm }} />
              <Text variant="title3" weight="700">Slippage Tolerance</Text>
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
                <View>
                  <Text variant="bodySm" color="textSecondary">Auto</Text>
                  <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>0.5% — recommended</Text>
                </View>
                <Toggle value={slippageAuto} onValueChange={setSlippageAuto} accessibilityLabel="Auto slippage" />
              </View>
              {!slippageAuto && (
                <View
                  style={{
                    flexDirection: 'row', alignItems: 'center', borderWidth: 1, borderColor: colors.border,
                    borderRadius: radius.md, paddingHorizontal: spacing.lg, paddingVertical: spacing.md,
                  }}
                >
                  <RNTextInput
                    style={{ flex: 1, fontSize: 16, color: colors.textPrimary }}
                    value={String(Number(slippage) * 100)}
                    onChangeText={v => { const n = Number(v); if (!isNaN(n) && n >= 0.1 && n <= 49) setSlippage(String(n / 100)); }}
                    keyboardType="decimal-pad"
                    placeholder="Enter %"
                    placeholderTextColor={colors.textDisabled}
                  />
                  <Text variant="bodyMed" weight="600" color="textMuted">%</Text>
                </View>
              )}
            </Pressable>
          </Pressable>
        </Modal>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
}

function SwapSuccessScreen({ fromToken, toToken, fromAmount, outputAmount, txHash, chainId, onDone }: {
  fromToken: LiFiToken | null; toToken: LiFiToken | null;
  fromAmount: string; outputAmount: string | null;
  txHash: string; chainId: number; onDone: () => void;
}) {
  const { colors, spacing, radius } = useTheme();
  const iconScale = useSharedValue(0);
  const ringScale = useSharedValue(1);
  const ringOpacity = useSharedValue(0);
  const contentOpacity = useSharedValue(0);
  const contentTranslateY = useSharedValue(28);
  const btnOpacity = useSharedValue(0);
  const btnTranslateY = useSharedValue(20);

  useEffect(() => {
    iconScale.value = withSpring(1, { damping: 11, stiffness: 160 });
    ringOpacity.value = withDelay(80, withSequence(
      withTiming(0.5, { duration: 100 }),
      withTiming(0, { duration: 550 }),
    ));
    ringScale.value = withDelay(80, withTiming(2.2, { duration: 650 }));
    contentOpacity.value = withDelay(320, withTiming(1, { duration: 380 }));
    contentTranslateY.value = withDelay(320, withSpring(0, { damping: 15 }));
    btnOpacity.value = withDelay(560, withTiming(1, { duration: 300 }));
    btnTranslateY.value = withDelay(560, withSpring(0, { damping: 14 }));
  }, []);

  const iconStyle = useAnimatedStyle(() => ({ transform: [{ scale: iconScale.value }] }));
  const ringStyle = useAnimatedStyle(() => ({ transform: [{ scale: ringScale.value }], opacity: ringOpacity.value }));
  const contentStyle = useAnimatedStyle(() => ({ opacity: contentOpacity.value, transform: [{ translateY: contentTranslateY.value }] }));
  const btnStyle = useAnimatedStyle(() => ({ opacity: btnOpacity.value, transform: [{ translateY: btnTranslateY.value }] }));

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flex: 1, justifyContent: 'center', padding: spacing['2xl'] }}>
        {/* Animated checkmark */}
        <View style={{ alignItems: 'center', marginBottom: spacing['3xl'] }}>
          <View style={{ width: 100, height: 100, alignItems: 'center', justifyContent: 'center' }}>
            <Animated.View style={[{
              position: 'absolute', width: 100, height: 100, borderRadius: 50,
              backgroundColor: colors.successBg,
            }, ringStyle]} />
            <Animated.View style={[{
              width: 72, height: 72, borderRadius: 36,
              backgroundColor: colors.success,
              alignItems: 'center', justifyContent: 'center',
              shadowColor: colors.success, shadowOffset: { width: 0, height: 6 },
              shadowOpacity: 0.3, shadowRadius: 12, elevation: 8,
            }, iconStyle]}>
              <Icon name="check" size={38} color={colors.textInverse} />
            </Animated.View>
          </View>
        </View>

        {/* Title + swap summary */}
        <Animated.View style={[{ alignItems: 'center' }, contentStyle]}>
          <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing['2xl'] }}>Swap Complete</Text>

          <Card padding="lg" style={{ width: '100%' }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.md }}>
              <TokenIcon uri={fromToken?.logoURI} symbol={fromToken?.symbol} size={36} />
              <View style={{ flex: 1 }}>
                <Text variant="title3" weight="700">{fromAmount} {fromToken?.symbol}</Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>{fromToken?.name}</Text>
              </View>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, paddingVertical: 2 }}>
              <View style={{ flex: 1, height: 1, backgroundColor: colors.hairline }} />
              <Icon name="arrowDown" size={14} color={colors.textMuted} />
              <View style={{ flex: 1, height: 1, backgroundColor: colors.hairline }} />
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.md }}>
              <TokenIcon uri={toToken?.logoURI} symbol={toToken?.symbol} size={36} />
              <View style={{ flex: 1 }}>
                <Text variant="title3" weight="700" color="accent">{outputAmount} {toToken?.symbol}</Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>{toToken?.name}</Text>
              </View>
            </View>
          </Card>

          {txHash ? (() => {
            const url = isSolanaLiFiChain(chainId)
              ? txExplorerUrl(txHash, { chainKey: 'solana' }) ?? null
              : (() => {
                  const explorerUrl = getChainByChainId(chainId)?.explorerUrl;
                  return explorerUrl ? `${explorerUrl}/tx/${txHash}` : null;
                })();
            return (
              <PressableScale
                style={{
                  flexDirection: 'row', alignItems: 'center', gap: 5, marginTop: spacing.lg,
                  padding: spacing.sm, borderRadius: radius.md, backgroundColor: colors.accentMuted,
                }}
                onPress={() => url && Linking.openURL(url)}
                disabled={!url}
              >
                <Icon name="arrowRight" size={13} color={colors.accent} />
                <Text variant="caption" weight="600" color="accent" numberOfLines={1} ellipsizeMode="middle" style={{ maxWidth: 220 }}>
                  {txHash.slice(0, 10)}…{txHash.slice(-8)}
                </Text>
              </PressableScale>
            );
          })() : null}
        </Animated.View>

        {/* Done button */}
        <Animated.View style={[{ width: '100%', marginTop: spacing['2xl'] }, btnStyle]}>
          <Button title="Done" onPress={onDone} size="lg" />
        </Animated.View>
      </View>
    </SafeAreaView>
  );
}

function SwapFailureScreen({ error, onRetry }: { error: string; onRetry: () => void }) {
  const { colors, spacing } = useTheme();
  const iconScale = useSharedValue(0);
  const contentOpacity = useSharedValue(0);

  useEffect(() => {
    iconScale.value = withSpring(1, { damping: 11, stiffness: 160 });
    contentOpacity.value = withDelay(280, withTiming(1, { duration: 350 }));
  }, []);

  const iconStyle = useAnimatedStyle(() => ({ transform: [{ scale: iconScale.value }] }));
  const contentStyle = useAnimatedStyle(() => ({ opacity: contentOpacity.value }));

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }}>
      <View style={{ flex: 1, justifyContent: 'center', padding: spacing['2xl'] }}>
        <View style={{ alignItems: 'center', marginBottom: spacing['3xl'] }}>
          <Animated.View style={[{
            width: 72, height: 72, borderRadius: 36,
            backgroundColor: colors.danger,
            alignItems: 'center', justifyContent: 'center',
            shadowColor: colors.danger, shadowOffset: { width: 0, height: 6 },
            shadowOpacity: 0.25, shadowRadius: 12, elevation: 8,
          }, iconStyle]}>
            <Icon name="close" size={36} color={colors.textInverse} />
          </Animated.View>
        </View>

        <Animated.View style={[{ alignItems: 'center', width: '100%' }, contentStyle]}>
          <Text variant="title1" weight="700" align="center" style={{ marginBottom: spacing.md }}>Swap Failed</Text>
          {error ? (
            <Card padding="lg" style={{ width: '100%', backgroundColor: colors.dangerBg }}>
              <Text variant="bodySm" align="center" style={{ color: colors.danger, lineHeight: 20 }}>{error}</Text>
            </Card>
          ) : null}
          <Button title="Try Again" onPress={onRetry} size="lg" style={{ marginTop: spacing['2xl'] }} />
        </Animated.View>
      </View>
    </SafeAreaView>
  );
}

function DetailRow({ label, value, highlight, last }: { label: string; value: string; highlight?: boolean; last?: boolean }) {
  const { colors } = useTheme();
  return (
    <View
      style={{
        flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', paddingVertical: 11,
        ...(last ? null : { borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: colors.hairline }),
      }}
    >
      <Text variant="bodySm" color="textSecondary">{label}</Text>
      <Text variant="bodySm" weight="600" style={highlight ? { color: colors.accent } : undefined}>{value}</Text>
    </View>
  );
}
