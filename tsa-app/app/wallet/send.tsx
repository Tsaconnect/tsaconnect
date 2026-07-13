// app/wallet/send.tsx
//
// Send crypto — recipient + amount + chain/token selection, then a
// review → authorize → sign + broadcast + confirm flow. Restyled onto the
// TSA Signature design system (@/theme tokens + @/components/ui primitives).
//
// FINANCIAL/SECURITY screen: this is a visual/structural restyle ONLY. Every
// piece of money handling is preserved verbatim — recipient-address parsing and
// validation (isValidAddress), amount parsing + the token↔USD input-mode
// conversion, chain/token selection (incl. the discovered-token nav-param path),
// gas estimation, and the prepareSendTransaction → signTransaction →
// submitTransaction confirm flow. No address or amount logic was altered.
import React, { useState, useEffect, useCallback } from 'react';
import {
  View, ScrollView, KeyboardAvoidingView, Platform, FlatList, Linking,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { parseUnits, formatUnits } from 'ethers';
import {
  CHAINS, CHAIN_KEYS, NONEVM_CHAINS, getChainDisplay, isNonEVMChain, txExplorerUrl,
  type ChainKey, type AnyChainKey,
} from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { isValidAddress, signTransaction, getActiveWallet } from '../../services/wallet';
import { isValidTronAddress, isValidBtcAddress, isValidSolanaAddress } from '../../services/chains/derivation';
import { getTronAddress, getTronBalances } from '../../services/chains/tronWallet';
import { prepareTron, broadcastTron, type SendTronParams, type TronPrepared } from '../../services/chains/tronSend';
import { getBtcAddress, getBtcBalances } from '../../services/chains/btcWallet';
import { prepareBtc, broadcastBtc, type SendBtcParams, type BtcPrepared } from '../../services/chains/btcSend';
import { getSolanaAddress, getSolanaBalances } from '../../services/chains/solanaWallet';
import { prepareSolana, broadcastSolana, type SendSolanaParams, type SolanaPrepared } from '../../services/chains/solanaSend';
import { getNetwork } from '../../hooks/useNetwork';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  sendPreparedTransaction,
  submitTransaction,
  type WalletBalanceWithChain,
} from '../../services/walletApi';
import AuthorizationModal from '../../components/common/AuthorizationModal';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  Button,
  Card,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Sheet,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';

type BalanceEntry = WalletBalanceWithChain;

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'failure';

// Round token-symbol badge. Color is sourced from the token registry's
// iconColor (data-driven, like wallet/home) and falls back to a neutral token.
const TokenBadge = ({
  symbol,
  size = 36,
  bg,
}: {
  symbol: string;
  size?: number;
  bg: string;
}) => (
  <View
    style={{
      width: size,
      height: size,
      borderRadius: size / 2,
      justifyContent: 'center',
      alignItems: 'center',
      backgroundColor: bg,
    }}
  >
    <Text variant="bodyMed" weight="700" style={{ fontSize: size * 0.38, color: '#FFFFFF' }}>
      {symbol[0]}
    </Text>
  </View>
);

const SendToken = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokens, tokenList } = useTokens();

  // Token-color map derived from the registry (matches wallet/home), so badge
  // colors stay data-driven instead of hardcoded hex.
  const tokenColorFor = useCallback(
    (symbol: string): string => tokens[symbol]?.iconColor || colors.textMuted,
    [tokens, colors.textMuted],
  );

  // Discovered-token params (set when arriving from the wallet/token detail
  // screen for an auto-discovered ERC-20). When present, we skip the
  // per-chain balance scan and prepare the tx with explicit contract +
  // decimals, since the token isn't in supported_tokens.
  const navParams = useLocalSearchParams<{
    token?: string;
    chainKey?: string;
    contractAddress?: string;
    discovered?: string;
    decimals?: string;
    balance?: string;
    usdValue?: string;
    name?: string;
  }>();
  const isDiscovered = navParams.discovered === '1' && !!navParams.contractAddress;
  const discoveredDecimals = navParams.decimals ? parseInt(navParams.decimals, 10) : undefined;

  const [selectedToken, setSelectedToken] = useState(navParams.token || 'USDT');
  const [selectedChain, setSelectedChain] = useState<AnyChainKey>(
    (navParams.chainKey as AnyChainKey) || 'sonic',
  );
  const [toAddress, setToAddress] = useState('');
  const [amount, setAmount] = useState('');
  const [inputMode, setInputMode] = useState<'token' | 'usd'>('token');
  const [balances, setBalances] = useState<BalanceEntry[]>([]);
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [showTokenPicker, setShowTokenPicker] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);
  // Prepared non-EVM tx carried from review → send so we don't rebuild it
  // (and re-run the fee preflight) a second time at broadcast.
  const [nonEvmTx, setNonEvmTx] = useState<
    | { family: 'tron'; prepared: TronPrepared['prepared']; params: SendTronParams }
    | { family: 'btc'; prepared: BtcPrepared['prepared']; params: SendBtcParams }
    | { family: 'solana'; prepared: SolanaPrepared['prepared']; params: SendSolanaParams }
    | null
  >(null);

  const nonEvmFamily = isNonEVMChain(selectedChain) ? NONEVM_CHAINS[selectedChain].family : null;
  const isTron = nonEvmFamily === 'tron';
  const isBtc = nonEvmFamily === 'btc';
  const isSolana = nonEvmFamily === 'solana';

  const getSupportedChainsForToken = useCallback((symbol: string): AnyChainKey[] => {
    const registryChains = tokens[symbol]?.chains ?? [];
    if (registryChains.length > 0) {
      return registryChains;
    }

    const inferredChains = Array.from(
      new Set(
        balances
          .filter((entry) => entry.symbol === symbol)
          .map((entry) => entry.chainKey)
      )
    );
    return inferredChains;
  }, [balances, tokens]);

  useEffect(() => {
    // Discovered tokens come pre-pinned to a single chain via nav params;
    // don't let the per-chain auto-correct overwrite that selection. A non-EVM
    // (Tron) selection is likewise explicit — the token registry only lists EVM
    // chains, so auto-correct would wrongly bounce it back to an EVM chain.
    if (isDiscovered || isNonEVMChain(selectedChain)) return;
    const chains = getSupportedChainsForToken(selectedToken);
    if (chains.length === 0) return;
    if (!chains.includes(selectedChain)) {
      setSelectedChain(chains[0]);
    }
  }, [getSupportedChainsForToken, selectedChain, selectedToken, isDiscovered]);

  const loadBalances = useCallback(async () => {
    // Discovered token: skip the multi-chain scan entirely (the token
    // isn't in supported_tokens, so it wouldn't appear) and seed a
    // single synthetic entry from the nav params.
    if (isDiscovered) {
      setBalances([{
        symbol: navParams.token || '',
        name: navParams.name || navParams.token || '',
        balance: navParams.balance || '0',
        usdValue: navParams.usdValue || '0',
        contractAddress: navParams.contractAddress || '',
        decimals: discoveredDecimals ?? 18,
        chainKey: (navParams.chainKey as ChainKey) || 'sonic',
        chainName: CHAINS[(navParams.chainKey as ChainKey) || 'sonic']?.shortName || (navParams.chainKey as string) || '',
      }]);
      setLoading(false);
      return;
    }
    try {
      setLoading(true);
      const chainResults = await Promise.all(
        CHAIN_KEYS.map(async (chainKey) => {
          const result = await getWalletBalances(CHAINS[chainKey].chainId);
          return { chainKey, result };
        })
      );

      const allBalances: BalanceEntry[] = [];
      const seen = new Set<string>();

      for (const { chainKey, result } of chainResults) {
        if (!result.success || !result.data) continue;
        for (const entry of normalizeWalletBalances(result.data, chainKey)) {
          allBalances.push(entry);
          seen.add(`${entry.symbol}-${entry.chainKey}`);
        }
      }

      // Add 0-balance entries for supported tokens not returned by API
      for (const t of tokenList) {
        for (const chain of t.chains) {
          const key = `${t.symbol}-${chain}`;
          if (!seen.has(key)) {
            allBalances.push({
              symbol: t.symbol, name: t.name, balance: '0', usdValue: '0.00',
              contractAddress: '', decimals: t.decimals,
              chainKey: chain as ChainKey, chainName: CHAINS[chain as ChainKey]?.shortName || chain,
            });
          }
        }
      }

      // Non-EVM (Tron/BTC/Solana): derive each address and append its balances so
      // TRX/USDT/BTC/SOL + SPL tokens appear in the token picker. Dormant-safe —
      // each chain yields nothing if unset.
      try {
        const tronAddr = await getTronAddress();
        if (tronAddr) allBalances.push(...(await getTronBalances(tronAddr)));
      } catch { /* chain unavailable — skip */ }
      try {
        const btcAddr = await getBtcAddress();
        if (btcAddr) allBalances.push(...(await getBtcBalances(btcAddr)));
      } catch { /* chain unavailable — skip */ }
      try {
        const solAddr = await getSolanaAddress();
        if (solAddr) allBalances.push(...(await getSolanaBalances(solAddr)));
      } catch { /* chain unavailable — skip */ }

      setBalances(allBalances);
    } catch (err) {
      console.error('Send: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList, isDiscovered, navParams.token, navParams.name, navParams.balance, navParams.usdValue, navParams.contractAddress, navParams.chainKey, discoveredDecimals]);

  useEffect(() => { loadBalances(); }, [loadBalances]);

  const getBalance = (): string => {
    const entry = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
    return entry?.balance || '0';
  };
  const getUsdPrice = (): number => {
    const b = balances.find(b => b.symbol === selectedToken && b.chainKey === selectedChain);
    if (!b) return selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0;
    const bal = parseFloat(b.balance || '0');
    const usd = parseFloat(b.usdValue || '0');
    return bal > 0 ? usd / bal : (selectedToken === 'USDC' || selectedToken === 'USDT' ? 1 : 0);
  };

  const tokenAmount = inputMode === 'token' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) / getUsdPrice()).toFixed(6) : '0'
  );
  const usdAmount = inputMode === 'usd' ? amount : (
    amount && getUsdPrice() > 0 ? (parseFloat(amount) * getUsdPrice()).toFixed(2) : '0'
  );

  const activeChain = getChainDisplay(selectedChain);
  const gasSymbol = activeChain?.nativeCurrency?.symbol || 'S';

  const validAddress = (addr: string): boolean => {
    if (isTron) return isValidTronAddress(addr);
    if (isBtc) return isValidBtcAddress(addr, getNetwork());
    if (isSolana) return isValidSolanaAddress(addr);
    return isValidAddress(addr);
  };

  const handlePaste = async () => {
    try {
      const text = await Clipboard.getStringAsync();
      if (text) setToAddress(text.trim());
    } catch {}
  };

  const validate = (): string | null => {
    if (!toAddress.trim()) return 'Enter a recipient address.';
    if (!validAddress(toAddress.trim())) return 'Invalid wallet address.';
    if (!amount.trim() || isNaN(Number(amount)) || Number(amount) <= 0) return 'Enter a valid amount.';
    const sendAmount = parseFloat(tokenAmount);
    if (sendAmount > parseFloat(getBalance())) return 'Insufficient balance.';
    return null;
  };

  // Build the Tron send params for the selected token from the loaded balance
  // row (decimals + TRC-20 contract). Null when we can't derive the address.
  const buildTronParams = async (recipient: string): Promise<SendTronParams | null> => {
    const walletId = await getActiveWallet();
    const from = await getTronAddress(walletId || undefined);
    if (!from || !walletId) return null;
    const entry = balances.find((b) => b.symbol === selectedToken && b.chainKey === selectedChain);
    const decimals = entry?.decimals ?? 6;
    return {
      from,
      to: recipient,
      amount: parseUnits(tokenAmount, decimals).toString(),
      symbol: selectedToken,
      tokenAddr: entry?.contractAddress || '',
      walletAddress: walletId, // used to look up the mnemonic for Tron-key signing
    };
  };

  // Build the BTC send params (native only — no tokens on Bitcoin). Amount is
  // sats (8 decimals). Null when we can't derive the address.
  const buildBtcParams = async (recipient: string): Promise<SendBtcParams | null> => {
    const walletId = await getActiveWallet();
    const from = await getBtcAddress(walletId || undefined);
    if (!from || !walletId) return null;
    return {
      from,
      to: recipient,
      amount: parseUnits(tokenAmount, 8).toString(),
      symbol: 'BTC',
      walletAddress: walletId, // used to look up the mnemonic for BIP-84 signing
    };
  };

  // Build the Solana send params for the selected token from the loaded balance
  // row (SOL = 9 decimals; SPL uses the mint's decimals + mint address). Null
  // when we can't derive the address.
  const buildSolanaParams = async (recipient: string): Promise<SendSolanaParams | null> => {
    const walletId = await getActiveWallet();
    const from = await getSolanaAddress(walletId || undefined);
    if (!from || !walletId) return null;
    const entry = balances.find((b) => b.symbol === selectedToken && b.chainKey === selectedChain);
    // Native SOL is a safe 9-decimals default; an SPL send needs the mint's real
    // decimals from its balance row — guessing would mis-scale the amount by
    // orders of magnitude, so bail rather than default.
    const decimals = entry?.decimals ?? (selectedToken === 'SOL' ? 9 : undefined);
    if (decimals === undefined) return null;
    return {
      from,
      to: recipient,
      amount: parseUnits(tokenAmount, decimals).toString(),
      symbol: selectedToken,
      tokenAddr: entry?.contractAddress || '', // SPL mint; empty for native SOL
      walletAddress: walletId, // used to look up the mnemonic for ed25519 signing
    };
  };

  const handleReview = async () => {
    const e = validate(); if (e) { setError(e); return; }
    setError('');
    try {
      setLoading(true);
      if (isTron) {
        // Tron: the prepare endpoint runs the mandatory gas preflight and
        // returns the TRX fee. A 402 (no TRX for gas) throws with a friendly
        // note we surface on the form instead of advancing to confirm.
        const params = await buildTronParams(toAddress.trim());
        if (!params) throw new Error('Could not derive your Tron wallet.');
        const { prepared, fee } = await prepareTron(params);
        setNonEvmTx({ family: 'tron', prepared, params });
        setGasEstimate(formatUnits(fee.estFeeRaw || '0', fee.gasDecimals || 6));
        setScreen('confirm');
        return;
      }
      if (isBtc) {
        // BTC: prepare selects UTXOs server-side and returns the network fee.
        // A 402 (balance can't cover amount + fee) throws with a friendly note.
        const params = await buildBtcParams(toAddress.trim());
        if (!params) throw new Error('Could not derive your Bitcoin wallet.');
        const { prepared, fee } = await prepareBtc(params);
        setNonEvmTx({ family: 'btc', prepared, params });
        setGasEstimate(formatUnits(fee.estFeeRaw || '0', fee.gasDecimals || 8));
        setScreen('confirm');
        return;
      }
      if (isSolana) {
        // Solana: prepare runs the SOL fee preflight (base fee + ATA rent when
        // the recipient has no token account). A 402 (no SOL for gas) throws
        // with a friendly note we surface on the form.
        const params = await buildSolanaParams(toAddress.trim());
        if (!params) throw new Error('Could not derive your Solana wallet.');
        const { prepared, fee } = await prepareSolana(params);
        setNonEvmTx({ family: 'solana', prepared, params });
        setGasEstimate(formatUnits(fee.estFeeRaw || '0', fee.gasDecimals || 9));
        setScreen('confirm');
        return;
      }
      // EVM: spend from the ACTIVE device wallet — captured once and threaded
      // through prepare → sign → submit so the signer guard matches.
      const from = await getActiveWallet();
      if (!from) throw new Error('No wallet found. Set up your wallet first.');
      const r = await prepareSendTransaction(
        selectedToken,
        toAddress.trim(),
        tokenAmount,
        activeChain?.chainId || CHAINS.sonic.chainId,
        isDiscovered
          ? {
              tokenAddress: navParams.contractAddress,
              decimals: discoveredDecimals,
              fromAddress: from,
            }
          : { fromAddress: from },
      );
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      try {
        const gpStr = r.data.gasPrice || '0';
        const glStr = r.data.gasLimit || '21000';
        const gp = BigInt(gpStr);
        const gl = BigInt(glStr);
        const gasFee = Number(gp * gl) / 1e18;
        setGasEstimate(gasFee > 0 ? gasFee.toFixed(6) : '~0.001');
      } catch {
        setGasEstimate('~0.001');
      }
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSend = async () => {

    setScreen('sending'); setError('');
    const chainId = activeChain?.chainId || CHAINS.sonic.chainId;
    const recipient = toAddress.trim();
    try {
      // Non-EVM: broadcast the tx already built (and fee-checked) at review.
      // Signed locally with the chain-path key inside broadcastTron/Btc.
      // nonEvmTx is set by handleReview; rebuild defensively if missing.
      if (isTron) {
        let ctx = nonEvmTx?.family === 'tron' ? nonEvmTx : null;
        if (!ctx) {
          const params = await buildTronParams(recipient);
          if (!params) throw new Error('Could not derive your Tron wallet.');
          ctx = { family: 'tron', prepared: (await prepareTron(params)).prepared, params };
        }
        const { txHash: hash } = await broadcastTron(ctx.prepared, ctx.params);
        setTxHash(hash);
        setScreen('success');
        return;
      }
      if (isBtc) {
        let ctx = nonEvmTx?.family === 'btc' ? nonEvmTx : null;
        if (!ctx) {
          const params = await buildBtcParams(recipient);
          if (!params) throw new Error('Could not derive your Bitcoin wallet.');
          ctx = { family: 'btc', prepared: (await prepareBtc(params)).prepared, params };
        }
        const { txHash: hash } = await broadcastBtc(ctx.prepared, ctx.params);
        setTxHash(hash);
        setScreen('success');
        return;
      }
      if (isSolana) {
        let ctx = nonEvmTx?.family === 'solana' ? nonEvmTx : null;
        if (!ctx) {
          const params = await buildSolanaParams(recipient);
          if (!params) throw new Error('Could not derive your Solana wallet.');
          ctx = { family: 'solana', prepared: (await prepareSolana(params)).prepared, params };
        }
        const { txHash: hash } = await broadcastSolana(ctx.prepared, ctx.params);
        setTxHash(hash);
        setScreen('success');
        return;
      }
      // EVM: ONE captured address through prepare → sign → submit — a mid-flow
      // wallet switch must fail the signature pin, not pair another account's
      // nonce with this signature (queued-forever tx).
      const from = await getActiveWallet();
      if (!from) throw new Error('No wallet found. Set up your wallet first.');
      // Nonce is computed server-side at prepare time and can go stale before
      // broadcast on Sonic's load-balanced RPC ("nonce too low"). The signed tx
      // can't be patched, so on a nonce error we re-prepare (fresh nonce),
      // re-sign, and retry once.
      const res = await sendPreparedTransaction({
        prepare: () =>
          prepareSendTransaction(
            selectedToken,
            recipient,
            tokenAmount,
            chainId,
            isDiscovered
              ? {
                  tokenAddress: navParams.contractAddress,
                  decimals: discoveredDecimals,
                  fromAddress: from,
                }
              : { fromAddress: from },
          ),
        sign: (tx) =>
          signTransaction({
            type: 0, // legacy transaction format
            to: tx.to, data: tx.data, value: tx.value,
            gasLimit: tx.gasLimit, gasPrice: tx.gasPrice,
            nonce: tx.nonce, chainId: tx.chainId,
          }, from),
        submit: (signed) =>
          submitTransaction(signed, 'send', selectedToken, recipient, tokenAmount, chainId, from),
      });
      if (res.success && res.data?.txHash) { setTxHash(res.data.txHash); setScreen('success'); }
      else throw new Error(res.message || 'Failed');
    } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
  };

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
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.lg,
              backgroundColor: ok ? colors.successBg : colors.dangerBg,
            }}
          >
            <Icon name={ok ? 'checkCircle' : 'closeCircle'} size={48} color={ok ? colors.success : colors.danger} />
          </View>
          <Text variant="title1" weight="700" align="center">
            {ok ? 'Sent!' : 'Failed'}
          </Text>
          <Text
            variant="bodySm"
            color="textSecondary"
            align="center"
            style={{ marginTop: spacing.xs, marginBottom: spacing.xs, paddingHorizontal: spacing['2xl'] }}
          >
            {ok ? `${tokenAmount} ${selectedToken} sent` : error}
          </Text>
          {ok && (
            <Text variant="bodySm" color="textMuted" align="center" style={{ marginBottom: spacing.xl }}>
              ≈ ${usdAmount}
            </Text>
          )}
          {txHash ? (
            <Text
              variant="caption"
              color="textMuted"
              numeric
              numberOfLines={1}
              ellipsizeMode="middle"
              style={{ maxWidth: '80%', marginBottom: spacing.md }}
            >
              Tx: {txHash}
            </Text>
          ) : null}
          {ok && txHash ? (() => {
            const url = txExplorerUrl(txHash, nonEvmFamily ? { chainKey: selectedChain } : { chainId: activeChain?.chainId });
            return url ? (
              <Button
                title="View on explorer"
                variant="ghost"
                leftIcon="nearMe"
                onPress={() => Linking.openURL(url)}
                fullWidth={false}
                style={{ alignSelf: 'center', marginBottom: spacing.md }}
              />
            ) : null;
          })() : null}
          <Button
            title="Back to Wallet"
            onPress={() => router.replace('/tokenization')}
            fullWidth={false}
            style={{ alignSelf: 'center', marginTop: spacing.lg }}
          />
        </View>
      </SafeAreaView>
    );
  }

  // ── Sending ──
  if (screen === 'sending') return (
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
        <Text variant="title1" weight="700" align="center">
          Sending
        </Text>
        <Text variant="bodySm" weight="600" color="accent" align="center" style={{ marginTop: spacing.xs }}>
          {tokenAmount} {selectedToken} → {toAddress.slice(0, 8)}...
        </Text>
      </View>
    </SafeAreaView>
  );

  // ── Confirm ──
  if (screen === 'confirm') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <PressableScale
          onPress={() => setScreen('form')}
          style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.lg }}
        >
          <Icon name="chevronLeft" size={22} color={colors.accent} />
          <Text variant="bodyMed" weight="600" color="accent">
            Back
          </Text>
        </PressableScale>
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>
          Confirm Send
        </Text>

        <Card>
          <SummaryCard>
            <SummaryRow
              label="Amount"
              value={
                <View style={{ alignItems: 'flex-end' }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <TokenBadge symbol={selectedToken} size={24} bg={tokenColorFor(selectedToken)} />
                    <Text variant="title3" weight="700" numeric>
                      {tokenAmount} {selectedToken}
                    </Text>
                  </View>
                  <Text variant="caption" color="textMuted" numeric style={{ marginTop: 2 }}>
                    ≈ ${usdAmount}
                  </Text>
                </View>
              }
            />
            <Divider spacing="xs" />
            <SummaryRow
              label="To"
              value={
                <Text variant="bodyMed" weight="600" numeric numberOfLines={1} ellipsizeMode="middle" style={{ maxWidth: 180 }}>
                  {toAddress}
                </Text>
              }
            />
            <SummaryRow label="Network" value={activeChain?.name || selectedChain} />
            <SummaryRow label="Gas Fee" value={`${gasEstimate} ${gasSymbol}`} />
          </SummaryCard>
        </Card>

        <Button
          title="Confirm & Send"
          leftIcon="send"
          onPress={() => setShowAuthModal(true)}
          size="lg"
          style={{ marginTop: spacing.lg }}
        />
        <Button
          title="Cancel"
          variant="ghost"
          onPress={() => setScreen('form')}
          style={{ marginTop: spacing.sm }}
        />
      </ScrollView>

      <AuthorizationModal
        visible={showAuthModal}
        title="Authorize Transfer"
        description={`Send ${tokenAmount} ${selectedToken} to ${toAddress.slice(0, 8)}...${toAddress.slice(-4)}`}
        onAuthorized={() => { setShowAuthModal(false); handleSend(); }}
        onCancel={() => setShowAuthModal(false)}
      />
    </SafeAreaView>
  );

  // ── Form ──
  const price = getUsdPrice();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          <Text variant="title1" weight="700">
            Send
          </Text>
          <Text variant="bodySm" color="textMuted" style={{ marginTop: spacing.xs, marginBottom: spacing.lg }}>
            Transfer tokens to any wallet
          </Text>

          {/* Amount card */}
          <Card style={{ marginBottom: spacing.md }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
              <Text variant="overline" color="textMuted">
                AMOUNT
              </Text>
              <PressableScale
                onPress={() => {
                  setInputMode('token');
                  setAmount(getBalance());
                }}
                hitSlop={6}
              >
                <Text variant="caption" weight="600" color="accent" numeric>
                  Balance: {getBalance()} {selectedToken}
                </Text>
              </PressableScale>
            </View>

            {/* Token selector + Amount input */}
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <PressableScale
                onPress={() => setShowTokenPicker(true)}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.surfaceAlt,
                  paddingHorizontal: spacing.sm,
                  paddingVertical: spacing.sm,
                  borderRadius: radius.pill,
                }}
              >
                <TokenBadge symbol={selectedToken} size={28} bg={tokenColorFor(selectedToken)} />
                <Text variant="bodyMed" weight="700">
                  {selectedToken}
                </Text>
                <Icon name="chevronDown" size={16} color={colors.textMuted} />
              </PressableScale>
              <View style={{ flex: 1 }}>
                <AmountInput
                  placeholder="0.00"
                  value={amount}
                  onChangeText={(t) => { setAmount(t); setError(''); }}
                  inputStyle={{ textAlign: 'right' }}
                />
              </View>
            </View>

            {/* USD equivalent + toggle */}
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginTop: spacing.md }}>
              <Text variant="bodySm" color="textMuted" numeric>
                {inputMode === 'token'
                  ? `≈ $${amount && price > 0 ? (parseFloat(amount) * price).toFixed(2) : '0.00'}`
                  : `≈ ${amount && price > 0 ? (parseFloat(amount) / price).toFixed(6) : '0'} ${selectedToken}`}
              </Text>
              <PressableScale
                onPress={() => {
                  if (inputMode === 'token' && price > 0 && amount) {
                    setAmount((parseFloat(amount) * price).toFixed(2));
                  } else if (inputMode === 'usd' && price > 0 && amount) {
                    setAmount((parseFloat(amount) / price).toFixed(6));
                  }
                  setInputMode(m => m === 'token' ? 'usd' : 'token');
                }}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.xs,
                  backgroundColor: colors.accentMuted,
                  paddingHorizontal: spacing.sm,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.pill,
                }}
              >
                <Icon name="swapVertical" size={16} color={colors.accent} />
                <Text variant="caption" weight="600" color="accent">
                  {inputMode === 'token' ? 'USD' : selectedToken}
                </Text>
              </PressableScale>
            </View>
          </Card>

          {/* Recipient */}
          <Card style={{ marginBottom: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.md }}>
              RECIPIENT
            </Text>
            <Input
              placeholder={isTron ? 'T... wallet address' : isBtc ? 'bc1... wallet address' : isSolana ? 'Solana wallet address' : '0x... wallet address'}
              value={toAddress}
              onChangeText={(t) => { setToAddress(t); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
              rightAdornment={
                <IconButton
                  icon="copy"
                  accessibilityLabel="Paste address"
                  color={colors.accent}
                  onPress={handlePaste}
                />
              }
              error={toAddress && !validAddress(toAddress) ? 'Invalid address format' : undefined}
            />
          </Card>

          {/* Network info */}
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginBottom: spacing.md, marginLeft: spacing.xs }}>
            <Icon name="info" size={16} color={colors.textMuted} />
            <Text variant="bodySm" color="textMuted">
              {activeChain?.name || selectedChain}
            </Text>
          </View>

          {error ? (
            <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg, marginBottom: spacing.sm }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="warning" size={16} color={colors.danger} />
                <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                  {error}
                </Text>
              </View>
            </AccentCard>
          ) : null}

          <Button
            title="Review"
            leftIcon="send"
            onPress={handleReview}
            loading={loading}
            disabled={loading}
            size="lg"
            style={{ marginTop: spacing.md }}
          />
        </ScrollView>

        {/* Token + Chain Picker */}
        <Sheet visible={showTokenPicker} onClose={() => setShowTokenPicker(false)} title="Select Token">
          <FlatList
            data={[...(balances as BalanceEntry[])].sort((a, b) => parseFloat(b.balance) - parseFloat(a.balance))}
            keyExtractor={item => `${item.symbol}-${item.chainKey}`}
            contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.lg }}
            keyboardShouldPersistTaps="handled"
            renderItem={({ item }) => {
              const active = item.symbol === selectedToken && item.chainKey === selectedChain;
              const bal = parseFloat(item.balance);
              return (
                <PressableScale
                  onPress={() => {
                    setSelectedToken(item.symbol);
                    setSelectedChain(item.chainKey);
                    setShowTokenPicker(false);
                    setInputMode('token');
                    setAmount('');
                  }}
                  style={{
                    flexDirection: 'row',
                    alignItems: 'center',
                    paddingVertical: spacing.md,
                    paddingHorizontal: spacing.sm,
                    borderRadius: radius.md,
                    backgroundColor: active ? colors.accentMuted : 'transparent',
                  }}
                >
                  <TokenBadge symbol={item.symbol} size={36} bg={tokenColorFor(item.symbol)} />
                  <View style={{ flex: 1, marginLeft: spacing.md }}>
                    <Text variant="bodyMed" weight="700">
                      {item.symbol}
                    </Text>
                    <Text variant="caption" color="textMuted">
                      {item.name} · {item.chainName}
                    </Text>
                  </View>
                  <View style={{ alignItems: 'flex-end' }}>
                    <Text variant="bodySm" weight="600" numeric>
                      {bal > 0 ? bal.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0'}
                    </Text>
                    {parseFloat(item.usdValue) > 0 && (
                      <Text variant="caption" color="textMuted" numeric>
                        ${parseFloat(item.usdValue).toFixed(2)}
                      </Text>
                    )}
                  </View>
                </PressableScale>
              );
            }}
          />
        </Sheet>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default SendToken;
