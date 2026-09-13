// screens/swap.tsx
// Token swap screen — extensible for multiple pairs, currently MCGP/USDC.
// Migrated onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). All swap logic, amount/decimal/fee math, price polling, token
// selection, navigation routes, and the signed-but-not-broadcast tx flow are
// unchanged — this is a visual/structural restyle only. Per-token brand colors
// (USDC blue, USDT green, MCGP gold) come from the TOKENS data config, not ad-hoc
// styling, so they're preserved as token identity.
//
// This is the OTC desk's TRADE FORM. The catalogue that leads here — the
// Direct/Presale sub-tabs, the listing rows, the admin's logo and asset class —
// lives in components/otc/OtcListingList; this screen is pushed once the user
// has picked a row and a side, so it opens on `side` and no longer owns the
// pair-selection chrome.
//
// It still re-checks the admin pause (/api/otc/settings, fail OPEN) rather than
// trusting the list: the route is addressable, so a deep link can reach the
// form directly. A paused direction dims Review and explains itself in the
// same sheet the list uses.
import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  View, TextInput, ScrollView, KeyboardAvoidingView, Platform, FlatList,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { CHAINS } from '@/constants/chains';
import { signTransaction, withRpcFallback } from '@/services/wallet';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import {
  getSwapPrice, prepareSwap, submitTransaction, getWalletBalances, normalizeWalletBalances,
  getOtcSettings, type OtcSettings, type WalletBalance,
} from '@/services/walletApi';
import { useTheme } from '@/theme';
import { NoLocalWalletScreen } from '@/components/wallet/NoLocalWallet';
import {
  Text, Button, Card, Icon, PressableScale, Sheet, Spinner,
  ScreenHeader,
} from '@/components/ui';
import { mapSubmitError, SWAP_COPY } from '@/services/submitErrors';
import { OtcClosedSheet } from '@/components/otc/OtcClosedSheet';

// ── Token config (extend this list as new tokens are added) ──
interface TokenInfo {
  symbol: string;
  name: string;
  decimals: number;
  color: string;
  icon: string; // first letter or symbol
}

const TOKENS: Record<string, TokenInfo> = {
  MCGP: { symbol: 'MCGP', name: 'MCG Protocol', decimals: 18, color: '#D4AF37', icon: 'M' },
  USDC: { symbol: 'USDC', name: 'USD Coin', decimals: 6, color: '#2775CA', icon: '$' },
  USDT: { symbol: 'USDT', name: 'Tether', decimals: 6, color: '#26A17B', icon: '₮' },
};

// Available swap pairs: [fromToken, toToken]
// Add new pairs here as contracts support them
const SWAP_PAIRS: Array<{ from: string; to: string; label: string }> = [
  { from: 'USDC', to: 'MCGP', label: 'Buy MCGP with USDC' },
  { from: 'MCGP', to: 'USDC', label: 'Sell MCGP for USDC' },
  // Future: { from: 'USDT', to: 'MCGP', label: 'Buy MCGP with USDT' },
];

type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

function toWei(amount: string, d: number): bigint {
  try {
    const [int, frac = ''] = amount.split('.');
    return BigInt(int || '0') * BigInt(10 ** d) + BigInt(frac.padEnd(d, '0').slice(0, d) || '0');
  } catch { return BigInt(0); }
}

function fromWei(wei: string, d: number, show = 6): string {
  try {
    const n = BigInt(wei), f = BigInt(10 ** d);
    const frac = (n % f).toString().padStart(d, '0').slice(0, show);
    return `${n / f}.${frac}`.replace(/\.?0+$/, '') || '0';
  } catch { return '0'; }
}

// Native-token gas estimate (Sonic = S, 18 decimals) for the approve + swap txs.
// Each tx's max cost is gasLimit * gasPrice (wei); we sum them and format.
function estimateGasFee(
  approveTx: { gasLimit: string; gasPrice: string } | null | undefined,
  swapTx: { gasLimit: string; gasPrice: string },
): string | null {
  try {
    const cost = (tx: { gasLimit: string; gasPrice: string }) =>
      BigInt(tx.gasLimit || '0') * BigInt(tx.gasPrice || '0');
    let total = cost(swapTx);
    if (approveTx) total += cost(approveTx);
    if (total <= BigInt(0)) return null;
    return fromWei(total.toString(), 18, 6);
  } catch { return null; }
}

// ── Token Badge ── (per-token brand color from config)
const TokenBadge = ({ token, size = 40 }: { token: TokenInfo; size?: number }) => (
  <View
    style={{
      width: size,
      height: size,
      borderRadius: size / 2,
      backgroundColor: token.color,
      justifyContent: 'center',
      alignItems: 'center',
    }}
  >
    <Text style={{ fontSize: size * 0.38, fontWeight: '700', color: '#FFF' }}>{token.icon}</Text>
  </View>
);

// ── Token Selector Button ──
const TokenSelector = ({ token, onPress, disabled }: { token: TokenInfo; onPress: () => void; disabled?: boolean }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      disabled={disabled}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.xs,
        backgroundColor: colors.surfaceAlt,
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.sm,
        borderRadius: radius.xl,
      }}
    >
      <TokenBadge token={token} size={32} />
      <Text variant="bodyMed" weight="700">{token.symbol}</Text>
      {!disabled && <Icon name="chevronDown" size={16} color={colors.textMuted} />}
    </PressableScale>
  );
};

// ── Token Picker Sheet ──
const TokenPicker = ({
  visible, tokens, selected, onSelect, onClose,
}: {
  visible: boolean;
  tokens: TokenInfo[];
  selected: string;
  onSelect: (symbol: string) => void;
  onClose: () => void;
}) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <Sheet visible={visible} onClose={onClose} title="Select Token">
      <FlatList
        data={tokens}
        keyExtractor={t => t.symbol}
        style={{ flexShrink: 1 }}
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.md }}
        renderItem={({ item }) => {
          const isSelected = item.symbol === selected;
          return (
            <PressableScale
              onPress={() => { onSelect(item.symbol); onClose(); }}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                paddingVertical: spacing.md,
                paddingHorizontal: spacing.sm,
                borderRadius: radius.md,
                backgroundColor: isSelected ? colors.accentMuted : 'transparent',
              }}
            >
              <TokenBadge token={item} size={36} />
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodyMed" weight="700">{item.symbol}</Text>
                <Text variant="caption" color="textMuted">{item.name}</Text>
              </View>
              {isSelected && <Icon name="checkCircle" size={22} color={colors.accent} />}
            </PressableScale>
          );
        }}
      />
    </Sheet>
  );
};

export interface SwapScreenProps {
  /** Which way the user chose to trade in the listing. Buying MCGP pays USDC. */
  initialSide?: 'buy' | 'sell';
}

// ── Main Screen ──
const SwapScreen: React.FC<SwapScreenProps> = ({ initialSide = 'buy' }) => {
  const { colors, spacing, radius, fonts } = useTheme();

  // null until loaded — treated as everything-enabled (fail OPEN).
  const [otcSettings, setOtcSettings] = useState<OtcSettings | null>(null);
  // `direction` is derived from which side is being paid, so the side the user
  // picked is expressed by seeding the pair rather than by a separate flag.
  const [fromSymbol, setFromSymbol] = useState(initialSide === 'sell' ? 'MCGP' : 'USDC');
  const [toSymbol, setToSymbol] = useState(initialSide === 'sell' ? 'USDC' : 'MCGP');
  const [amount, setAmount] = useState('');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loadingBal, setLoadingBal] = useState(true);
  // Balances are only ever read for the wallet this device holds a key for.
  // Falling back to the account's registered address would render MCGP the
  // user cannot sign for — the screen that made us look custodial.
  const { address: signer, ready: signerReady } = useSignableWallet();
  const [priceLoading, setPriceLoading] = useState(false);
  const [quote, setQuote] = useState<{ mcgp: string; usdc: string; rate: string } | null>(null);
  const [priceErr, setPriceErr] = useState('');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [status, setStatus] = useState('');
  const [pickerFor, setPickerFor] = useState<'from' | 'to' | null>(null);
  // Set when a paused direction's Review is pressed — drives the closed notice.
  const [closedSide, setClosedSide] = useState<'buy' | 'sell' | null>(null);
  // Prepared swap (fetched on Review so the confirm screen can show real gas).
  const [prepared, setPrepared] = useState<Awaited<ReturnType<typeof prepareSwap>>['data'] | null>(null);
  // Wallet the prepared txs' nonces belong to — sign/submit must reuse it.
  const [preparedFor, setPreparedFor] = useState('');
  const [gasFee, setGasFee] = useState<string | null>(null);
  const [preparing, setPreparing] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fromToken = TOKENS[fromSymbol];
  const toToken = TOKENS[toSymbol];
  const direction = fromSymbol === 'MCGP' ? 'sell' as const : 'buy' as const;

  const loadBalances = useCallback(async () => {
    if (!signer) { setBalances([]); setLoadingBal(false); return; }
    try {
      setLoadingBal(true);
      // Query Sonic chain specifically since swaps happen on Sonic
      const sonicChainId = CHAINS.sonic.chainId;
      const r = await getWalletBalances(sonicChainId, signer);
      if (r.success && r.data) {
        const fetched: WalletBalance[] = normalizeWalletBalances(r.data, 'sonic').map(
          ({ chainKey, chainName, ...balance }) => balance
        );
        // Merge with known swap tokens so all always appear
        const balanceMap = new Map(fetched.map(b => [b.symbol, b]));
        const merged: WalletBalance[] = Object.values(TOKENS).map(t =>
          balanceMap.get(t.symbol) ?? { symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00', contractAddress: '', decimals: t.decimals }
        );
        // Also include any other tokens from the API
        for (const b of fetched) {
          if (!merged.some(m => m.symbol === b.symbol)) merged.push(b);
        }
        setBalances(merged);
      }
    } catch (err) {
      console.error('Swap: load balances error:', err);
    } finally { setLoadingBal(false); }
  }, [signer]);

  // Refresh balances every time the screen gains focus
  useFocusEffect(useCallback(() => { loadBalances(); }, [loadBalances]));

  // Refresh the admin OTC controls on focus (getOtcSettings caches briefly and
  // fails OPEN, so a backend hiccup never locks the desk).
  useFocusEffect(useCallback(() => {
    let live = true;
    getOtcSettings().then(s => { if (live) setOtcSettings(s); });
    return () => { live = false; };
  }, []));

  const bal = (sym: string) => balances.find(b => b.symbol === sym)?.balance || '0';

  const fetchPrice = useCallback((dir: 'buy' | 'sell', input: string, fromDec: number) => {
    if (timer.current) clearTimeout(timer.current);
    if (!input || isNaN(Number(input)) || Number(input) <= 0) {
      setQuote(null); setPriceErr(''); return;
    }
    timer.current = setTimeout(async () => {
      setPriceLoading(true); setPriceErr('');
      try {
        const params = dir === 'buy'
          ? { usdcAmount: toWei(input, fromDec).toString() }
          : { mcgpAmount: toWei(input, fromDec).toString() };
        const r = await getSwapPrice(dir, params);
        if (r.success && r.data) {
          setQuote({ mcgp: r.data.mcgpAmount, usdc: r.data.usdcAmount, rate: r.data.pricePerMCGP });
        } else {
          setPriceErr(r.message || 'Price unavailable'); setQuote(null);
        }
      } catch (e: any) { setPriceErr(e.message || 'Failed'); }
      finally { setPriceLoading(false); }
    }, 500);
  }, []);

  useEffect(() => {
    // Any input change invalidates a previously-prepared swap + its gas estimate.
    setPrepared(null); setPreparedFor(''); setGasFee(null);
    fetchPrice(direction, amount, fromToken.decimals);
    return () => { if (timer.current) clearTimeout(timer.current); };
  }, [amount, direction, fromToken.decimals, fetchPrice]);

  const rawOutput = quote
    ? direction === 'buy' ? fromWei(quote.mcgp, TOKENS.MCGP.decimals, 4) : fromWei(quote.usdc, TOKENS.USDC.decimals, 4)
    : null;
  const outputDisplay = rawOutput && rawOutput !== '0' ? rawOutput : null;
  const rawRate = quote ? fromWei(quote.rate, TOKENS.USDC.decimals, 6) : null;
  const rateDisplay = rawRate && rawRate !== '0' ? rawRate : null;

  const swapTokens = () => {
    const newFrom = toSymbol;
    const newTo = fromSymbol;
    setFromSymbol(newFrom);
    setToSymbol(newTo);
    setAmount(''); setQuote(null); setPriceErr(''); setError('');
  };

  const selectFrom = (sym: string) => {
    if (sym === toSymbol) setToSymbol(fromSymbol); // swap if same
    setFromSymbol(sym);
    setAmount(''); setQuote(null); setError('');
  };

  const selectTo = (sym: string) => {
    if (sym === fromSymbol) setFromSymbol(toSymbol);
    setToSymbol(sym);
    setAmount(''); setQuote(null); setError('');
  };

  // Available tokens for picker (based on existing pairs)
  const fromTokenOptions = [...new Set(SWAP_PAIRS.map(p => p.from))].map(s => TOKENS[s]);
  const toTokenOptions = [...new Set(SWAP_PAIRS.map(p => p.to))].map(s => TOKENS[s]);

  const validate = (): string | null => {
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    if (Number(amount) > Number(bal(fromSymbol))) return `Insufficient ${fromSymbol} balance.`;
    if (!quote) return 'Waiting for price...';
    return null;
  };

  // Prepare the swap up front (on Review) so the confirm screen can show the
  // real network gas fee instead of hiding it until after the user signs.
  const goToConfirm = async () => {
    const e = validate();
    if (e) { setError(e); return; }
    setError(''); setPreparing(true);
    try {
      const params = direction === 'buy'
        ? { usdcAmount: toWei(amount, fromToken.decimals).toString() }
        : { mcgpAmount: toWei(amount, fromToken.decimals).toString() };
      const from = signer;
      if (!from) throw new Error('No wallet on this device. Import your recovery phrase to swap.');
      const prep = await prepareSwap(direction, { ...params, slippageBps: 50 }, from);
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Failed to prepare swap');
      setPrepared(prep.data);
      setPreparedFor(from);
      setGasFee(estimateGasFee(prep.data.approveTx, prep.data.swapTx));
      setScreen('confirm');
    } catch (err: any) {
      setError(err.message || 'Failed to prepare swap.');
    } finally { setPreparing(false); }
  };

  // Admin pause for the active direction — explicit false only, so unloaded /
  // failed settings leave the desk open (fail OPEN).
  const directionPaused = direction === 'buy'
    ? otcSettings?.buyEnabled === false
    : otcSettings?.sellEnabled === false;

  // Review press while paused: no navigation, just the explanation. The button
  // stays pressable (dimmed via style) so the tap can explain itself.
  const onReviewPress = () => {
    if (directionPaused) {
      setClosedSide(direction);
      return;
    }
    goToConfirm();
  };

  const handleSwap = async () => {

    setScreen('signing'); setError('');
    try {
      const params = direction === 'buy'
        ? { usdcAmount: toWei(amount, fromToken.decimals).toString() }
        : { mcgpAmount: toWei(amount, fromToken.decimals).toString() };

      // Reuse the txs prepared on the confirm step; only re-prepare if
      // missing. `from` MUST be the wallet the reused txs' nonces belong to —
      // signing/submitting with a different (freshly-resolved) wallet would
      // pair another account's nonce with this signature.
      let prepData = prepared;
      let from = preparedFor;
      if (!prepData || !from) {
        setStatus('Preparing swap...');
        from = signer || '';
        if (!from) throw new Error('No wallet on this device. Import your recovery phrase to swap.');
        const prep = await prepareSwap(direction, { ...params, slippageBps: 50 }, from);
        if (!prep.success || !prep.data) throw new Error(prep.message || 'Failed to prepare');
        prepData = prep.data;
      }

      const { approveTx, swapTx } = prepData;

      if (approveTx) {
        setStatus('Approving tokens...');
        const signedA = await signTransaction({
          type: 0,
          to: approveTx.to, data: approveTx.data, value: approveTx.value,
          gasLimit: approveTx.gasLimit, gasPrice: approveTx.gasPrice,
          nonce: approveTx.nonce, chainId: approveTx.chainId,
        }, from);

        // submitTransaction returns { success: false, message } on backend
        // errors — it never throws. Check explicitly so a failed approve
        // surfaces here instead of silently advancing into the swap step
        // with a stale nonce.
        const approveRes = await submitTransaction(signedA, 'approve', fromSymbol, approveTx.to, amount, approveTx.chainId, from);
        const approveHash = approveRes.success ? approveRes.data?.txHash : undefined;
        if (!approveHash) {
          console.error('[swap] approve rejected by backend', approveRes);
          throw new Error(mapSubmitError(approveRes.message, SWAP_COPY));
        }

        // Wait for the approve to confirm before submitting the swap.
        // Without this we'd push two pending txs from the same sender into
        // the mempool back-to-back. Nodes that enforce EIP-7702's
        // in-flight-tx cap for delegated EOAs (BSC Maxwell+, Pectra) reject
        // the second. The wait is the correct pattern regardless of the
        // cap — it also surfaces approve failures that would otherwise be
        // hidden behind a stale-nonce error on the swap step.
        setStatus('Waiting for approval to confirm...');
        let receipt;
        try {
          receipt = await withRpcFallback('sonic', (p) =>
            p.waitForTransaction(approveHash, 1, 90_000),
          );
        } catch (e) {
          console.error('[swap] waitForTransaction failed', { approveHash, e });
          throw new Error(`Lost connection while waiting for the approval (tx ${approveHash}). Try again.`);
        }
        if (!receipt) {
          throw new Error(`Approval is taking longer than expected (tx ${approveHash}). Check the network and try again once it confirms.`);
        }
        if (receipt.status === 0) {
          console.error('[swap] approve reverted on-chain', { approveHash, receipt });
          throw new Error(`Approval reverted on-chain (tx ${approveHash}). Try again.`);
        }
      }

      setStatus('Executing swap...');
      const swapNonce = approveTx && swapTx.nonce <= approveTx.nonce
        ? approveTx.nonce + 1
        : swapTx.nonce;
      const signedS = await signTransaction({
        type: 0,
        to: swapTx.to, data: swapTx.data, value: swapTx.value,
        gasLimit: swapTx.gasLimit, gasPrice: swapTx.gasPrice,
        nonce: swapNonce, chainId: swapTx.chainId,
      }, from);
      const res = await submitTransaction(signedS, 'swap', 'MCGP', swapTx.to, amount, swapTx.chainId, from);

      const swapHash = res.success ? res.data?.txHash : undefined;
      if (swapHash) {
        setTxHash(swapHash); setScreen('success'); loadBalances();
      } else {
        console.error('[swap] swap rejected by backend', res);
        throw new Error(mapSubmitError(res.message, SWAP_COPY));
      }
    } catch (e: any) {
      console.error('[swap] handleSwap failed', e);
      setError(e.message || 'Swap failed.'); setScreen('failure');
    }
  };

  // ── Signing ──
  if (screen === 'signing') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
        <View
          style={{
            width: 80,
            height: 80,
            borderRadius: radius.pill,
            backgroundColor: colors.accentMuted,
            justifyContent: 'center',
            alignItems: 'center',
            marginBottom: spacing.lg,
          }}
        >
          <Spinner size="large" />
        </View>
        <Text variant="title1" weight="700">Processing Swap</Text>
        <Text variant="bodyMed" weight="600" color="accent" style={{ marginTop: spacing.sm, marginBottom: spacing.xl }}>
          {status}
        </Text>
        <View style={{ gap: spacing.xs, marginTop: spacing.md }}>
          <Text variant="bodySm" color="textSecondary">1. Approve {fromSymbol} spend</Text>
          <Text variant="bodySm" color="textSecondary">2. Execute swap on-chain</Text>
        </View>
      </View>
    </SafeAreaView>
  );

  // ── Result ──
  if (screen === 'success' || screen === 'failure') {
    const ok = screen === 'success';
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', padding: spacing['2xl'] }}>
          <View
            style={{
              width: 80,
              height: 80,
              borderRadius: radius.pill,
              backgroundColor: ok ? colors.successBg : colors.dangerBg,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
            }}
          >
            <Icon name={ok ? 'checkCircle' : 'closeCircle'} size={48} color={ok ? colors.success : colors.danger} />
          </View>
          <Text variant="title1" weight="700">{ok ? 'Swap Complete!' : 'Swap Failed'}</Text>
          <Text
            variant="bodyMed"
            color="textSecondary"
            align="center"
            style={{ marginTop: spacing.sm, marginBottom: spacing.xl, paddingHorizontal: spacing['2xl'] }}
          >
            {ok ? `Swapped ${amount} ${fromSymbol} → ${outputDisplay || ''} ${toSymbol}` : error}
          </Text>
          {txHash ? (
            <Text
              variant="caption"
              color="textMuted"
              numberOfLines={1}
              ellipsizeMode="middle"
              style={{ marginBottom: spacing['2xl'], maxWidth: '80%' }}
            >
              Tx: {txHash}
            </Text>
          ) : null}
          <Button
            title={ok ? 'Back to Wallet' : 'Try Again'}
            onPress={() => ok ? router.replace('/tokenization') : setScreen('form')}
            style={{ alignSelf: 'stretch' }}
          />
        </View>
      </SafeAreaView>
    );
  }

  // ── Confirm ──
  if (screen === 'confirm') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ flexGrow: 1, padding: spacing.xl, paddingBottom: spacing['4xl'] }}>
        <ScreenHeader
          onBack={() => setScreen('form')}
          style={{ paddingHorizontal: 0, marginBottom: spacing.lg }}
        />
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.xl }}>Confirm Swap</Text>

        <Card padding="lg">
          <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.md }}>
            <TokenBadge token={fromToken} />
            <View style={{ flex: 1, marginLeft: spacing.md }}>
              <Text variant="caption" color="textMuted" style={{ textTransform: 'uppercase', letterSpacing: 0.5 }}>You Pay</Text>
              <Text variant="title2" weight="700" style={{ marginTop: 2 }}>{amount} {fromSymbol}</Text>
            </View>
          </View>
          <View style={{ alignItems: 'center', paddingVertical: spacing.xs }}>
            <Icon name="arrowDown" size={20} color={colors.accent} />
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.md }}>
            <TokenBadge token={toToken} />
            <View style={{ flex: 1, marginLeft: spacing.md }}>
              <Text variant="caption" color="textMuted" style={{ textTransform: 'uppercase', letterSpacing: 0.5 }}>You Receive</Text>
              <Text variant="title2" weight="700" style={{ marginTop: 2 }}>{outputDisplay || '—'} {toSymbol}</Text>
            </View>
          </View>
          <View style={{ height: 1, backgroundColor: colors.hairline, marginVertical: spacing.md }} />
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">Rate</Text>
            <Text variant="bodySm" weight="600">1 MCGP = {rateDisplay || '—'} USDC</Text>
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">Slippage</Text>
            <Text variant="bodySm" weight="600">0.5%</Text>
          </View>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', paddingVertical: spacing.xs }}>
            <Text variant="bodySm" color="textSecondary">Network fee (est.)</Text>
            <Text variant="bodySm" weight="600">{gasFee ? `~${gasFee} S` : '—'}</Text>
          </View>
        </Card>

        <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
          Two transactions: token approval + swap execution. Gas is paid in S and
          may vary with network conditions.
        </Text>
        <Button
          title="Confirm Swap"
          leftIcon="swapVertical"
          onPress={handleSwap}
          style={{ marginTop: spacing.lg }}
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

  // ── No wallet on this device ──
  // Deliberately not a zero balance: a zero reads as "your wallet is empty",
  // which sends the user looking for missing funds instead of for their seed
  // phrase. MCGP lives on-chain against a key we do not have here.
  //
  // The shared component, not the inline copy this screen used to carry: that
  // copy existed only to keep the OTC tab header above it, and this branch
  // removed those tabs. Every other MCGP screen already gates this way.
  if (signerReady && !signer) return <NoLocalWalletScreen action="swap" />;

  // ── Form ──
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ flexGrow: 1, padding: spacing.xl, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* Title and asset class live on the native header and the listing row
              respectively; the live rate is the only chrome this form owns. */}
          <Stack.Screen options={{ title: direction === 'buy' ? 'Buy MCGP' : 'Sell MCGP' }} />
          <View style={{ flexDirection: 'row', justifyContent: 'flex-end', alignItems: 'flex-start', marginBottom: spacing['2xl'] }}>
            {rateDisplay ? (
              <View
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.accentMuted,
                  paddingHorizontal: spacing.md,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.pill,
                }}
              >
                <Icon name="trendingUp" size={14} color={colors.accent} />
                <Text variant="caption" weight="600" color="accent">1 MCGP = ${rateDisplay}</Text>
              </View>
            ) : null}
          </View>

          {/* From card */}
          <Card padding="lg">
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
              <Text variant="overline" color="textMuted">YOU PAY</Text>
              <PressableScale onPress={() => setAmount(bal(fromSymbol))}>
                <Text variant="caption" weight="500" color="accent">Balance: {bal(fromSymbol)}</Text>
              </PressableScale>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <TokenSelector
                token={fromToken}
                onPress={() => setPickerFor('from')}
                disabled={fromTokenOptions.length <= 1}
              />
              <TextInput
                style={{
                  flex: 1,
                  fontSize: 28,
                  fontFamily: fonts['700'],
                  color: colors.textPrimary,
                  textAlign: 'right',
                  padding: 0,
                }}
                placeholder="0.00"
                placeholderTextColor={colors.textMuted}
                value={amount}
                onChangeText={t => { setAmount(t); setError(''); }}
                keyboardType="decimal-pad"
              />
            </View>
          </Card>

          {/* Swap arrow */}
          <View style={{ alignItems: 'center', zIndex: 10, marginVertical: -12 }}>
            <PressableScale
              onPress={swapTokens}
              style={{
                width: 40,
                height: 40,
                borderRadius: radius.pill,
                backgroundColor: colors.accent,
                justifyContent: 'center',
                alignItems: 'center',
              }}
            >
              <Icon name="swapVertical" size={22} color={colors.onAccent} />
            </PressableScale>
          </View>

          {/* To card */}
          <Card padding="lg" style={{ marginTop: -8 }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
              <Text variant="overline" color="textMuted">YOU RECEIVE</Text>
              <Text variant="caption" weight="500" color="accent">Balance: {bal(toSymbol)}</Text>
            </View>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <TokenSelector
                token={toToken}
                onPress={() => setPickerFor('to')}
                disabled={toTokenOptions.length <= 1}
              />
              {priceLoading ? (
                <View style={{ flex: 1, alignItems: 'flex-end' }}>
                  <Spinner size="small" />
                </View>
              ) : (
                <Text
                  variant="display"
                  weight="700"
                  numeric
                  align="right"
                  style={{ flex: 1, fontSize: 28, lineHeight: 34 }}
                >
                  {outputDisplay || '—'}
                </Text>
              )}
            </View>
          </Card>

          {/* Info bar */}
          {priceErr ? (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                backgroundColor: colors.dangerBg,
                borderWidth: 1,
                borderColor: colors.danger,
                borderRadius: radius.sm,
                padding: spacing.md,
                marginTop: spacing.md,
              }}
            >
              <Icon name="warning" size={16} color={colors.danger} />
              <Text variant="caption" color="danger" style={{ flex: 1 }}>{priceErr}</Text>
            </View>
          ) : rateDisplay ? (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                backgroundColor: colors.accentMuted,
                borderWidth: 1,
                borderColor: colors.accent,
                borderRadius: radius.sm,
                padding: spacing.md,
                marginTop: spacing.md,
              }}
            >
              <Icon name="info" size={16} color={colors.accent} />
              <Text variant="caption" color="textSecondary" style={{ flex: 1 }}>
                1 MCGP = {rateDisplay} USDC  ·  0.5% slippage
              </Text>
            </View>
          ) : null}

          {error ? (
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.xs,
                backgroundColor: colors.dangerBg,
                borderWidth: 1,
                borderColor: colors.danger,
                borderRadius: radius.sm,
                padding: spacing.md,
                marginTop: spacing.md,
              }}
            >
              <Icon name="dispute" size={16} color={colors.danger} />
              <Text variant="caption" color="danger" style={{ flex: 1 }}>{error}</Text>
            </View>
          ) : null}

          {/* Action — while the active direction is admin-paused the button is
              dimmed but stays pressable so the tap can explain the reason. */}
          <Button
            title={preparing ? 'Calculating fees…' : 'Review Swap'}
            onPress={onReviewPress}
            loading={preparing}
            disabled={!directionPaused && (!amount || priceLoading || preparing || !!priceErr)}
            style={{ marginTop: spacing.xl, ...(directionPaused ? { opacity: 0.4 } : null) }}
          />
        </ScrollView>

        {/* Token picker sheets */}
        <TokenPicker
          visible={pickerFor === 'from'}
          tokens={fromTokenOptions}
          selected={fromSymbol}
          onSelect={selectFrom}
          onClose={() => setPickerFor(null)}
        />
        <TokenPicker
          visible={pickerFor === 'to'}
          tokens={toTokenOptions}
          selected={toSymbol}
          onSelect={selectTo}
          onClose={() => setPickerFor(null)}
        />
        <OtcClosedSheet side={closedSide} onClose={() => setClosedSide(null)} />
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default SwapScreen;
