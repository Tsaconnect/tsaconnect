// app/wallet/send.tsx
//
// Send crypto — token/chain + amount + recipient, then a review → authorize →
// sign + broadcast → receipt flow, on the TSA Signature design system.
//
// FINANCIAL/SECURITY screen. The money handling is unchanged from the previous
// version: recipient-address validation (isValidAddress and the non-EVM
// validators), amount parsing + the token↔USD conversion, the discovered-token
// nav-param path, every non-EVM prepare/broadcast, and the EVM
// prepareSendTransaction → signTransaction → submitTransaction flow. What the
// form adds is read-only: a debounced prepare to show the fee before Review
// (its result is never signed), and a gas check against the loaded balances.
// The receipt now waits for an EVM transfer to be mined before saying "sent",
// the same bounded wait Instant Pay uses.
import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  View, ScrollView, KeyboardAvoidingView, Platform, FlatList, Image,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import * as Clipboard from 'expo-clipboard';
import { parseUnits, formatUnits, type TransactionReceipt } from 'ethers';
import {
  CHAINS, CHAIN_KEYS, NONEVM_CHAINS, getChainDisplay, isNonEVMChain, txExplorerUrl,
  type ChainKey, type AnyChainKey,
} from '../../constants/chains';
import { useTokens } from '../../hooks/useTokens';
import { isValidAddress, signTransaction, getActiveWallet, getProvider } from '../../services/wallet';
import { isValidTronAddress, isValidBtcAddress, isValidSolanaAddress, isValidSuiAddress } from '../../services/chains/derivation';
import { getTronAddress, getTronBalances } from '../../services/chains/tronWallet';
import { prepareTron, broadcastTron, type SendTronParams, type TronPrepared } from '../../services/chains/tronSend';
import { getBtcAddress, getBtcBalances } from '../../services/chains/btcWallet';
import { prepareBtc, broadcastBtc, type SendBtcParams, type BtcPrepared } from '../../services/chains/btcSend';
import { getSolanaAddress, getSolanaBalances } from '../../services/chains/solanaWallet';
import { prepareSolana, broadcastSolana, type SendSolanaParams, type SolanaPrepared } from '../../services/chains/solanaSend';
import { getSuiAddress, getSuiBalances } from '../../services/chains/suiWallet';
import { prepareSui, broadcastSui, type SendSuiParams, type SuiPrepared } from '../../services/chains/suiSend';
import { getNetwork } from '../../hooks/useNetwork';
import {
  getWalletBalances,
  normalizeWalletBalances,
  prepareSendTransaction,
  sendPreparedTransaction,
  submitTransaction,
  type WalletBalanceWithChain,
} from '../../services/walletApi';
import { txHashOrThrow, TRANSFER_COPY } from '../../services/submitErrors';
import {
  feePaidNative,
  formatNativeAmount,
  hasGasShortfall,
  nativeBalanceOn,
  pickDefaultSendAsset,
  preparedFeeNative,
  reviewBlocker,
  sendOutcome,
  shortAddress,
} from '../../utils/sendForm';
import AuthorizationModal from '../../components/common/AuthorizationModal';
import TokenAvatar from '../../components/wallet/TokenAvatar';
import SendResult from '../../components/wallet/SendResult';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountInput,
  BottomActionBar,
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
  ScreenHeader,
} from '@/components/ui';

type BalanceEntry = WalletBalanceWithChain;

type Screen = 'form' | 'confirm' | 'sending' | 'success' | 'pending' | 'failure';

type FormFee =
  | { status: 'idle' | 'loading' | 'unavailable' }
  | { status: 'ready'; native: number };

const formatBalance = (value: string) => {
  const n = parseFloat(value);
  return n > 0 ? n.toLocaleString(undefined, { maximumFractionDigits: 6 }) : '0';
};

const SendToken = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokens, tokenList } = useTokens();

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
  // EVM chains whose balance call succeeded. A native row on any other chain is
  // a zero-filled placeholder, which must not read as "no gas".
  const [loadedChains, setLoadedChains] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(true);
  const [gasEstimate, setGasEstimate] = useState('0.001');
  const [feeIsActual, setFeeIsActual] = useState(false);
  const [formFee, setFormFee] = useState<FormFee>({ status: 'idle' });
  const [screen, setScreen] = useState<Screen>('form');
  const [error, setError] = useState('');
  const [txHash, setTxHash] = useState('');
  const [sentAt, setSentAt] = useState<Date | null>(null);
  const [showTokenPicker, setShowTokenPicker] = useState(false);
  const [showAuthModal, setShowAuthModal] = useState(false);
  // The opening asset is decided once, from the first balance load; after
  // that (or once the user picks) the selection is theirs.
  const assetDecidedRef = useRef(false);
  // Prepared non-EVM tx carried from review → send so we don't rebuild it
  // (and re-run the fee preflight) a second time at broadcast.
  const [nonEvmTx, setNonEvmTx] = useState<
    | { family: 'tron'; prepared: TronPrepared['prepared']; params: SendTronParams }
    | { family: 'btc'; prepared: BtcPrepared['prepared']; params: SendBtcParams }
    | { family: 'solana'; prepared: SolanaPrepared['prepared']; params: SendSolanaParams }
    | { family: 'sui'; prepared: SuiPrepared['prepared']; params: SendSuiParams }
    | null
  >(null);

  const nonEvmFamily = isNonEVMChain(selectedChain) ? NONEVM_CHAINS[selectedChain].family : null;
  const isTron = nonEvmFamily === 'tron';
  const isBtc = nonEvmFamily === 'btc';
  const isSolana = nonEvmFamily === 'solana';
  const isSui = nonEvmFamily === 'sui';
  const isEvm = nonEvmFamily === null;

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
      const loaded = new Set<string>();

      for (const { chainKey, result } of chainResults) {
        if (!result.success || !result.data) continue;
        loaded.add(chainKey);
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

      // Non-EVM (Tron/BTC/Solana/Sui): derive each address and append its balances
      // so TRX/USDT/BTC/SOL/SUI + tokens appear in the token picker. Dormant-safe —
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
      try {
        const suiAddr = await getSuiAddress();
        if (suiAddr) allBalances.push(...(await getSuiBalances(suiAddr)));
      } catch { /* chain unavailable — skip */ }

      setBalances(allBalances);
      setLoadedChains(loaded);
    } catch (err) {
      console.error('Send: load balances error:', err);
    } finally { setLoading(false); }
  }, [tokenList, isDiscovered, navParams.token, navParams.name, navParams.balance, navParams.usdValue, navParams.contractAddress, navParams.chainKey, discoveredDecimals]);

  useEffect(() => { loadBalances(); }, [loadBalances]);

  useEffect(() => {
    if (assetDecidedRef.current || isDiscovered || loading || balances.length === 0) return;
    assetDecidedRef.current = true;
    const pick = pickDefaultSendAsset(balances, {
      symbol: navParams.token,
      chainKey: navParams.chainKey as AnyChainKey | undefined,
    });
    if (!pick) return;
    setSelectedToken(pick.symbol);
    setSelectedChain(pick.chainKey);
  }, [balances, loading, isDiscovered, navParams.token, navParams.chainKey]);

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
  const balance = getBalance();

  const validAddress = (addr: string): boolean => {
    if (isTron) return isValidTronAddress(addr);
    if (isBtc) return isValidBtcAddress(addr, getNetwork());
    if (isSolana) return isValidSolanaAddress(addr);
    if (isSui) return isValidSuiAddress(addr);
    return isValidAddress(addr);
  };
  const recipient = toAddress.trim();
  const addressValid = !!recipient && validAddress(recipient);

  // Gas: EVM only — each non-EVM prepare runs its own fee preflight at Review.
  const sendingNative = selectedToken.toUpperCase() === gasSymbol.toUpperCase();
  const gasBalance = isEvm && loadedChains.has(selectedChain)
    ? nativeBalanceOn(balances, selectedChain, gasSymbol)
    : null;
  const nativeUsdPrice = (() => {
    const row = balances.find(b => b.chainKey === selectedChain && b.symbol.toUpperCase() === gasSymbol.toUpperCase());
    const bal = parseFloat(row?.balance || '0');
    return bal > 0 ? parseFloat(row?.usdValue || '0') / bal : 0;
  })();
  const feeNative = formFee.status === 'ready' ? formFee.native : null;
  const gasShort = hasGasShortfall(gasBalance, feeNative, parseFloat(tokenAmount) || 0, sendingNative);

  const blocker = reviewBlocker({
    address: toAddress,
    addressValid,
    amount,
    tokenAmount,
    balance,
    gasBalance,
    fee: feeNative,
    nativeSymbol: gasSymbol,
    sendingNative,
  });

  // Fee preview: a debounced EVM prepare once the send is otherwise valid. Only
  // gasPrice × gasLimit is read from it — the tx that gets signed is prepared
  // again at broadcast, with a fresh nonce and price. It re-runs when the
  // chain, token or recipient changes, not on every amount keystroke: a
  // transfer's gas doesn't depend on the amount, and each prepare costs the
  // backend several RPC calls.
  const evmChainId = activeChain?.chainId || CHAINS.sonic.chainId;
  const amountNum = parseFloat(tokenAmount);
  const amountReady = amountNum > 0 && amountNum <= parseFloat(balance);
  const previewAmountRef = useRef(tokenAmount);
  previewAmountRef.current = tokenAmount;
  useEffect(() => {
    if (!isEvm || !addressValid || !amountReady) {
      setFormFee({ status: 'idle' });
      return;
    }
    let cancelled = false;
    setFormFee({ status: 'loading' });
    const timer = setTimeout(async () => {
      try {
        const from = await getActiveWallet();
        if (!from) throw new Error('no wallet');
        const r = await prepareSendTransaction(
          selectedToken,
          recipient,
          previewAmountRef.current,
          evmChainId,
          isDiscovered
            ? { tokenAddress: navParams.contractAddress, decimals: discoveredDecimals, fromAddress: from }
            : { fromAddress: from },
        );
        if (cancelled) return;
        const native = r.success && r.data ? preparedFeeNative(r.data.gasPrice, r.data.gasLimit) : 0;
        setFormFee(native > 0 ? { status: 'ready', native } : { status: 'unavailable' });
      } catch {
        if (!cancelled) setFormFee({ status: 'unavailable' });
      }
    }, 700);
    return () => { cancelled = true; clearTimeout(timer); };
  }, [isEvm, addressValid, amountReady, recipient, selectedToken, evmChainId, isDiscovered, navParams.contractAddress, discoveredDecimals]);

  const handlePaste = async () => {
    try {
      const text = await Clipboard.getStringAsync();
      if (text) { setToAddress(text.trim()); setError(''); }
    } catch {}
  };

  const handleMax = () => {
    setInputMode('token');
    setError('');
    // Sending the chain's own coin: leave the fee behind when we know it, with
    // headroom for the price moving before broadcast.
    if (sendingNative && feeNative !== null) {
      const spendable = Math.max(0, parseFloat(balance) - feeNative * 1.5);
      setAmount(spendable > 0 ? spendable.toFixed(8).replace(/\.?0+$/, '') : '0');
      return;
    }
    setAmount(balance);
  };

  const openPicker = () => setShowTokenPicker(true);

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

  // Build the Sui send params for the selected token from the loaded balance row
  // (SUI = 9 decimals; a coin uses its own decimals + coin type). Null when we
  // can't derive the address.
  const buildSuiParams = async (recipient: string): Promise<SendSuiParams | null> => {
    const walletId = await getActiveWallet();
    const from = await getSuiAddress(walletId || undefined);
    if (!from || !walletId) return null;
    const entry = balances.find((b) => b.symbol === selectedToken && b.chainKey === selectedChain);
    // Native SUI is a safe 9-decimals default; a coin send needs the coin type's
    // real decimals from its balance row — guessing would mis-scale the amount,
    // so bail rather than default.
    const decimals = entry?.decimals ?? (selectedToken === 'SUI' ? 9 : undefined);
    if (decimals === undefined) return null;
    return {
      from,
      to: recipient,
      amount: parseUnits(tokenAmount, decimals).toString(),
      symbol: selectedToken,
      tokenAddr: entry?.contractAddress || '', // coin type; empty for native SUI
      walletAddress: walletId, // used to look up the mnemonic for ed25519 signing
    };
  };

  const handleReview = async () => {
    if (blocker) { setError(blocker); return; }
    setError('');
    setFeeIsActual(false);
    try {
      setLoading(true);
      if (isTron) {
        // Tron: the prepare endpoint runs the mandatory gas preflight and
        // returns the TRX fee. A 402 (no TRX for gas) throws with a friendly
        // note we surface on the form instead of advancing to confirm.
        const params = await buildTronParams(recipient);
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
        const params = await buildBtcParams(recipient);
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
        const params = await buildSolanaParams(recipient);
        if (!params) throw new Error('Could not derive your Solana wallet.');
        const { prepared, fee } = await prepareSolana(params);
        setNonEvmTx({ family: 'solana', prepared, params });
        setGasEstimate(formatUnits(fee.estFeeRaw || '0', fee.gasDecimals || 9));
        setScreen('confirm');
        return;
      }
      if (isSui) {
        // Sui: prepare builds the tx server-side and runs the SUI fee preflight
        // (gas is always paid in SUI). A 402 (no SUI for gas) throws with a
        // friendly note we surface on the form.
        const params = await buildSuiParams(recipient);
        if (!params) throw new Error('Could not derive your Sui wallet.');
        const { prepared, fee } = await prepareSui(params);
        setNonEvmTx({ family: 'sui', prepared, params });
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
        recipient,
        tokenAmount,
        evmChainId,
        isDiscovered
          ? {
              tokenAddress: navParams.contractAddress,
              decimals: discoveredDecimals,
              fromAddress: from,
            }
          : { fromAddress: from },
      );
      if (!r.success || !r.data) throw new Error(r.message || 'Failed to prepare transaction.');
      const fee = preparedFeeNative(r.data.gasPrice || '0', r.data.gasLimit || '21000');
      setGasEstimate(fee > 0 ? formatNativeAmount(fee) : '~0.001');
      setScreen('confirm');
    } catch (err: any) { setError(err.message || 'Failed to estimate gas.'); }
    finally { setLoading(false); }
  };

  const handleSend = async () => {

    setScreen('sending'); setError('');
    const chainId = evmChainId;
    try {
      // Non-EVM: broadcast the tx already built (and fee-checked) at review.
      // Signed locally with the chain-path key inside broadcastTron/Btc.
      // nonEvmTx is set by handleReview; rebuild defensively if missing.
      // These report "on its way": nothing here waits for their confirmation.
      if (isTron) {
        let ctx = nonEvmTx?.family === 'tron' ? nonEvmTx : null;
        if (!ctx) {
          const params = await buildTronParams(recipient);
          if (!params) throw new Error('Could not derive your Tron wallet.');
          ctx = { family: 'tron', prepared: (await prepareTron(params)).prepared, params };
        }
        const { txHash: hash } = await broadcastTron(ctx.prepared, ctx.params);
        setTxHash(hash); setSentAt(new Date());
        setScreen('pending');
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
        setTxHash(hash); setSentAt(new Date());
        setScreen('pending');
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
        setTxHash(hash); setSentAt(new Date());
        setScreen('pending');
        return;
      }
      if (isSui) {
        let ctx = nonEvmTx?.family === 'sui' ? nonEvmTx : null;
        if (!ctx) {
          const params = await buildSuiParams(recipient);
          if (!params) throw new Error('Could not derive your Sui wallet.');
          ctx = { family: 'sui', prepared: (await prepareSui(params)).prepared, params };
        }
        const { txHash: hash } = await broadcastSui(ctx.prepared, ctx.params);
        setTxHash(hash); setSentAt(new Date());
        setScreen('pending');
        return;
      }
      // EVM: ONE captured address through prepare → sign → submit — a mid-flow
      // wallet switch must fail the signature pin, not pair another account's
      // nonce with this signature (queued-forever tx).
      const from = await getActiveWallet();
      if (!from) throw new Error('No wallet found. Set up your wallet first.');
      // Nonce and gas price are both computed server-side at prepare time and
      // can go stale before broadcast — the nonce on Sonic's load-balanced RPC,
      // the gas price on any chain that reprices per block. The signed tx can't
      // be patched, so sendPreparedTransaction re-prepares, re-signs and retries
      // once on either.
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
      const hash = txHashOrThrow(res, TRANSFER_COPY);
      setTxHash(hash); setSentAt(new Date());

      // "Sent" must mean MINED, not merely broadcast — an accepted tx can sit in
      // the mempool (nonce queue, underpriced gas) and never arrive. Wait
      // bounded; a timeout or a failed read is the honest "on its way".
      let receipt: TransactionReceipt | null = null;
      try {
        receipt = await getProvider(selectedChain as ChainKey).waitForTransaction(hash, 1, 90_000);
      } catch {
        // The tx may still confirm; sendOutcome reads a missing receipt as pending.
      }
      const outcome = sendOutcome(receipt);
      if (outcome === 'reverted') {
        throw new Error('The transfer reverted on-chain. Nothing was sent beyond the network fee.');
      }
      if (receipt) {
        const paid = feePaidNative(receipt.gasUsed, receipt.gasPrice);
        if (paid > 0) { setGasEstimate(formatNativeAmount(paid)); setFeeIsActual(true); }
      }
      setScreen(outcome);
    } catch (e: any) { setError(e.message || 'Failed.'); setScreen('failure'); }
  };

  const resetForm = () => {
    setScreen('form');
    setAmount('');
    setToAddress('');
    setTxHash('');
    setSentAt(null);
    setError('');
    setInputMode('token');
    setFeeIsActual(false);
    loadBalances();
  };

  // ── Result ──
  if (screen === 'success' || screen === 'pending' || screen === 'failure') {
    return (
      <SendResult
        status={screen}
        amountLabel={`${tokenAmount} ${selectedToken}`}
        recipient={recipient}
        usdValue={usdAmount}
        networkName={activeChain?.name || selectedChain}
        feeLabel={`${feeIsActual ? '' : '≈ '}${gasEstimate} ${gasSymbol}`}
        sentAt={sentAt}
        txHash={txHash}
        explorerUrl={
          (txHash &&
            txExplorerUrl(txHash, nonEvmFamily ? { chainKey: selectedChain } : { chainId: activeChain?.chainId })) ||
          null
        }
        error={error}
        onDone={() => router.replace('/tokenization')}
        onSendAgain={resetForm}
        onTryAgain={() => setScreen('form')}
      />
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
        <Text variant="bodySm" weight="600" color="accent" align="center" numeric style={{ marginTop: spacing.xs }}>
          {tokenAmount} {selectedToken} → {shortAddress(recipient)}
        </Text>
        {isEvm ? (
          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.sm }}>
            Waiting for {activeChain?.name || 'the network'} to confirm. This can take up to a minute.
          </Text>
        ) : null}
      </View>
    </SafeAreaView>
  );

  // ── Confirm ──
  if (screen === 'confirm') return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <ScreenHeader
          onBack={() => setScreen('form')}
          style={{ paddingHorizontal: 0, marginBottom: spacing.lg }}
        />
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
                    <TokenAvatar
                      symbol={selectedToken}
                      iconUrl={tokens[selectedToken]?.iconUrl}
                      iconColor={tokens[selectedToken]?.iconColor}
                      chainKey={selectedChain}
                      size={24}
                    />
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
  const nativeGasFeeText = (() => {
    if (!isEvm) return 'Shown on review';
    switch (formFee.status) {
      case 'loading':
        return 'Estimating…';
      case 'ready': {
        const usd = formFee.native * nativeUsdPrice;
        const usdText = nativeUsdPrice > 0 ? (usd < 0.01 ? ' · <$0.01' : ` · $${usd.toFixed(2)}`) : '';
        return `≈ ${formatNativeAmount(formFee.native)} ${gasSymbol}${usdText}`;
      }
      case 'unavailable':
        return 'Shown on review';
      default:
        return '—';
    }
  })();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <KeyboardAvoidingView style={{ flex: 1 }} behavior={Platform.OS === 'ios' ? 'padding' : 'height'}>
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, gap: spacing.md }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          {/* You send */}
          <Card style={{ gap: spacing.md }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
              <Text variant="overline" color="textMuted">
                YOU SEND
              </Text>
              <Text variant="caption" color="textSecondary" numeric>
                Balance {formatBalance(balance)} {selectedToken}
              </Text>
            </View>

            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <PressableScale
                onPress={openPicker}
                accessibilityRole="button"
                accessibilityLabel={`Token: ${selectedToken} on ${activeChain?.name || selectedChain}. Change`}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.sm,
                  backgroundColor: colors.surfaceAlt,
                  paddingLeft: spacing.xs,
                  paddingRight: spacing.sm,
                  paddingVertical: spacing.xs,
                  borderRadius: radius.pill,
                }}
              >
                <TokenAvatar
                  symbol={selectedToken}
                  iconUrl={tokens[selectedToken]?.iconUrl}
                  iconColor={tokens[selectedToken]?.iconColor}
                  chainKey={selectedChain}
                  size={32}
                />
                <View>
                  <Text variant="bodyMed" weight="700">
                    {selectedToken}
                  </Text>
                  <Text variant="caption" color="textSecondary">
                    {activeChain?.shortName || selectedChain}
                  </Text>
                </View>
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

            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
              <Text variant="bodySm" color="textMuted" numeric>
                {inputMode === 'token'
                  ? `≈ $${amount && price > 0 ? (parseFloat(amount) * price).toFixed(2) : '0.00'}`
                  : `≈ ${amount && price > 0 ? (parseFloat(amount) / price).toFixed(6) : '0'} ${selectedToken}`}
              </Text>
              <View style={{ flexDirection: 'row', gap: spacing.xs }}>
                <PressableScale
                  onPress={handleMax}
                  accessibilityRole="button"
                  accessibilityLabel="Send your whole balance"
                  style={{
                    backgroundColor: colors.accentMuted,
                    paddingHorizontal: spacing.sm,
                    paddingVertical: spacing.xs,
                    borderRadius: radius.pill,
                  }}
                >
                  <Text variant="caption" weight="700" color="accent">
                    MAX
                  </Text>
                </PressableScale>
                <PressableScale
                  onPress={() => {
                    if (inputMode === 'token' && price > 0 && amount) {
                      setAmount((parseFloat(amount) * price).toFixed(2));
                    } else if (inputMode === 'usd' && price > 0 && amount) {
                      setAmount((parseFloat(amount) / price).toFixed(6));
                    }
                    setInputMode(m => m === 'token' ? 'usd' : 'token');
                  }}
                  accessibilityRole="button"
                  accessibilityLabel={inputMode === 'token' ? 'Enter the amount in US dollars' : `Enter the amount in ${selectedToken}`}
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
                  <Icon name="swapVertical" size={14} color={colors.accent} />
                  <Text variant="caption" weight="600" color="accent">
                    {inputMode === 'token' ? 'USD' : selectedToken}
                  </Text>
                </PressableScale>
              </View>
            </View>
          </Card>

          {/* To */}
          <Card style={{ gap: spacing.sm }}>
            <Text variant="overline" color="textMuted">
              TO
            </Text>
            <Input
              placeholder={isTron ? 'T... wallet address' : isBtc ? 'bc1... wallet address' : isSolana ? 'Solana wallet address' : isSui ? '0x... Sui address' : '0x... wallet address'}
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
              error={recipient && !addressValid ? `Not a valid ${activeChain?.name || ''} address` : undefined}
            />
            {addressValid ? (
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="checkCircle" size={14} color={colors.success} />
                <Text variant="caption" color="success">
                  Valid {activeChain?.name} address
                </Text>
              </View>
            ) : null}
          </Card>

          {/* Network, fee, gas */}
          <View style={{ gap: spacing.sm, paddingHorizontal: spacing.xs }}>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                {activeChain?.iconUrl ? (
                  <Image source={{ uri: activeChain.iconUrl }} style={{ width: 16, height: 16, borderRadius: 8 }} />
                ) : (
                  <View style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: activeChain?.iconColor || colors.textMuted }} />
                )}
                <Text variant="bodySm" weight="600">
                  {activeChain?.name || selectedChain}
                </Text>
              </View>
              {isDiscovered ? null : (
                <PressableScale onPress={openPicker} hitSlop={8}>
                  <Text variant="caption" weight="600" color="accent">
                    Change
                  </Text>
                </PressableScale>
              )}
            </View>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
              <Text variant="bodySm" color="textSecondary">
                Network fee
              </Text>
              <Text variant="bodySm" numeric>
                {nativeGasFeeText}
              </Text>
            </View>
            {isEvm && gasBalance !== null ? (
              <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center' }}>
                <Text variant="bodySm" color="textSecondary">
                  Your {gasSymbol} for fees
                </Text>
                <Text variant="bodySm" weight="600" numeric color={gasShort ? 'danger' : 'success'}>
                  {formatBalance(String(gasBalance))} · {gasShort ? 'not enough' : 'enough'}
                </Text>
              </View>
            ) : null}
          </View>

          {error ? (
            <AccentCard railColor={colors.danger} padding="md" style={{ backgroundColor: colors.dangerBg }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                <Icon name="warning" size={16} color={colors.danger} />
                <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                  {error}
                </Text>
              </View>
            </AccentCard>
          ) : null}
        </ScrollView>

        <BottomActionBar>
          <Button
            title="Review"
            leftIcon="send"
            onPress={handleReview}
            loading={loading}
            disabled={loading || !!blocker}
            size="lg"
          />
          {blocker && !loading && (amount || toAddress) ? (
            <Text variant="caption" color="textMuted" align="center">
              {blocker}
            </Text>
          ) : null}
        </BottomActionBar>

        {/* Token + Chain Picker */}
        <Sheet visible={showTokenPicker} onClose={() => setShowTokenPicker(false)} title="Select Token">
          <FlatList
            data={[...(balances as BalanceEntry[])].sort((a, b) => parseFloat(b.usdValue) - parseFloat(a.usdValue) || parseFloat(b.balance) - parseFloat(a.balance))}
            keyExtractor={item => `${item.symbol}-${item.chainKey}`}
            contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.lg }}
            keyboardShouldPersistTaps="handled"
            renderItem={({ item }) => {
              const active = item.symbol === selectedToken && item.chainKey === selectedChain;
              const bal = parseFloat(item.balance);
              return (
                <PressableScale
                  onPress={() => {
                    assetDecidedRef.current = true;
                    setSelectedToken(item.symbol);
                    setSelectedChain(item.chainKey);
                    setShowTokenPicker(false);
                    setInputMode('token');
                    setAmount('');
                    setError('');
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
                  <TokenAvatar
                    symbol={item.symbol}
                    iconUrl={tokens[item.symbol]?.iconUrl}
                    iconColor={tokens[item.symbol]?.iconColor}
                    chainKey={item.chainKey}
                    size={38}
                  />
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
