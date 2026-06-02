// screens/swap.tsx
// Token swap screen — extensible for multiple pairs, currently MCGP/USDC.
// Migrated onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). All swap logic, amount/decimal/fee math, price polling, token
// selection, navigation routes, and the signed-but-not-broadcast tx flow are
// unchanged — this is a visual/structural restyle only. Per-token brand colors
// (USDC blue, USDT green, MCGP gold) come from the TOKENS data config, not ad-hoc
// styling, so they're preserved as token identity.
import React, { useState, useEffect, useRef, useCallback } from 'react';
import {
  View, TextInput, ScrollView, KeyboardAvoidingView, Platform, FlatList,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useFocusEffect } from '@react-navigation/native';
import { CHAINS } from '@/constants/chains';
import { signTransaction } from '@/services/wallet';
import {
  getSwapPrice, prepareSwap, submitTransaction, getWalletBalances, normalizeWalletBalances,
  type WalletBalance,
} from '@/services/walletApi';
import { useTheme } from '@/theme';
import {
  Text, Button, Card, Icon, PressableScale, Sheet, Spinner,
} from '@/components/ui';

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

// ── Main Screen ──
const SwapScreen = () => {
  const { colors, spacing, radius, fonts } = useTheme();

  const [fromSymbol, setFromSymbol] = useState('USDC');
  const [toSymbol, setToSymbol] = useState('MCGP');
  const [amount, setAmount] = useState('');
  const [balances, setBalances] = useState<WalletBalance[]>([]);
  const [loadingBal, setLoadingBal] = useState(true);
  const [priceLoading, setPriceLoading] = useState(false);
  const [quote, setQuote] = useState<{ mcgp: string; usdc: string; rate: string } | null>(null);
  const [priceErr, setPriceErr] = useState('');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [status, setStatus] = useState('');
  const [pickerFor, setPickerFor] = useState<'from' | 'to' | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fromToken = TOKENS[fromSymbol];
  const toToken = TOKENS[toSymbol];
  const direction = fromSymbol === 'MCGP' ? 'sell' as const : 'buy' as const;

  const loadBalances = useCallback(async () => {
    try {
      setLoadingBal(true);
      // Query Sonic chain specifically since swaps happen on Sonic
      const sonicChainId = CHAINS.sonic.chainId;
      const r = await getWalletBalances(sonicChainId);
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
  }, []);

  // Refresh balances every time the screen gains focus
  useFocusEffect(useCallback(() => { loadBalances(); }, [loadBalances]));

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

  const handleSwap = async () => {

    setScreen('signing'); setError('');
    try {
      const params = direction === 'buy'
        ? { usdcAmount: toWei(amount, fromToken.decimals).toString() }
        : { mcgpAmount: toWei(amount, fromToken.decimals).toString() };

      setStatus('Preparing swap...');
      const prep = await prepareSwap(direction, { ...params, slippageBps: 50 });
      if (!prep.success || !prep.data) throw new Error(prep.message || 'Failed to prepare');

      const { approveTx, swapTx } = prep.data;

      if (approveTx) {
        setStatus('Approving tokens...');
        const signedA = await signTransaction({
          type: 0,
          to: approveTx.to, data: approveTx.data, value: approveTx.value,
          gasLimit: approveTx.gasLimit, gasPrice: approveTx.gasPrice,
          nonce: approveTx.nonce, chainId: approveTx.chainId,
        });
        await submitTransaction(signedA, 'approve', fromSymbol, approveTx.to, amount, approveTx.chainId);
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
      });
      const res = await submitTransaction(signedS, 'swap', 'MCGP', swapTx.to, amount, swapTx.chainId);

      if (res.success && res.data) { setTxHash(res.data.txHash); setScreen('success'); loadBalances(); }
      else throw new Error(res.message || 'Swap failed');
    } catch (e: any) { setError(e.message || 'Swap failed.'); setScreen('failure'); }
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
        <PressableScale
          onPress={() => setScreen('form')}
          style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.lg }}
        >
          <Icon name="chevronLeft" size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="600" color="accent">Back</Text>
        </PressableScale>
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
        </Card>

        <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.lg, marginBottom: spacing.sm }}>
          Two transactions: token approval + swap execution.
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

  // ── Form ──
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ flexGrow: 1, padding: spacing.xl, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* Header */}
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'flex-start', marginBottom: spacing['2xl'] }}>
            <View>
              <Text variant="title1" weight="700">Swap</Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: 2 }}>OTC Marketplace</Text>
            </View>
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

          {/* Action */}
          <Button
            title="Review Swap"
            onPress={() => { const e = validate(); if (e) setError(e); else { setError(''); setScreen('confirm'); } }}
            disabled={!amount || priceLoading || !!priceErr}
            style={{ marginTop: spacing.xl }}
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
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default SwapScreen;
