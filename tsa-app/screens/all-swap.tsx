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
  Modal, Pressable, Linking, StyleSheet, TextInput as RNTextInput,
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
import { getBtcAddress, getBtcBalances } from '@/services/chains/btcWallet';
import { getSuiAddress, getSuiBalances } from '@/services/chains/suiWallet';
import { ethers } from 'ethers';
import {
  getLiFiToken, getLiFiTokens, getLiFiAllChainTokens, getLiFiQuote, settleSwapFee, getSwapDelivery,
  type SwapDeliveryStatus,
  isSolanaLiFiChain, isBtcLiFiChain, isSuiLiFiChain, isNonEVMLiFiChain, SOLANA_NATIVE_ADDRESS,
  type LiFiToken, type LiFiQuote,
} from '@/services/lifi';
import { executeLiFiSwap } from '@/services/lifiFlow';
import { fundedQuoteChain } from '@/services/quoteChain';
import { missingAddressNotice } from '@/utils/swapAddresses';
import { resolveLiFiChainId } from '@/utils/spotChains';
import TokenPickerModal from '@/components/swap/TokenPickerModal';
import TokenAvatar from '@/components/wallet/TokenAvatar';
// LiFi tokens are identified by contract; only one we vouch for borrows a
// registry logo when LiFi sends none.
import { isKnownContract } from '@/utils/tokenLogo';
import { useTheme } from '@/theme';
import {
  Button,
  Card,
  Icon,
  IconButton,
  PressableScale,
  Text,
  Toggle,
  ScreenHeader,
} from '@/components/ui';

const DEFAULT_FROM_CHAIN_ID = 56;
const DEFAULT_TO_CHAIN_ID = 1;

/**
 * Both legs of a pair, named the way a Spot listing names them: a symbol plus
 * OUR chain key ('btc', 'bsc'). Cross-chain Spot listings arrive here because
 * their legs live on two chains and the same-chain desk cannot express that —
 * see services/spotTrade.
 */
export interface AllSwapPreselection {
  fromSymbol?: string;
  fromChain?: string;
  toSymbol?: string;
  toChain?: string;
  /** The leg to move onto the chain where the user holds its token. That leg's
   *  chain above is then only the fallback — see utils/quoteChain. */
  fundedLeg?: 'from' | 'to';
}

interface AllSwapScreenProps {
  /** Absent for the plain swap entry, which must behave exactly as before. */
  preselect?: AllSwapPreselection;
}

/**
 * The LiFi token for a symbol on one chain.
 *
 * Same two-step shape as the Spot desk's lookup (app/wallet/spot-trade) and for
 * the same reason: the single-token endpoint takes a symbol and answers small,
 * while the per-chain list is multi-MB and flaky on mobile — but that list is
 * the only fallback when LiFi's index does not answer for the symbol. Two
 * different shapes for the one problem would drift, so this one follows it.
 *
 * null means the leg genuinely cannot be selected, which the caller has to SAY:
 * a screen that keeps waiting on a symbol LiFi dropped never stops waiting.
 */
async function resolveLiFiSymbol(chainId: number, symbol: string): Promise<LiFiToken | null> {
  const wanted = symbol.trim().toUpperCase();
  if (!wanted) return null;
  // The exact symbol first, and aliases only on the chain they were checked
  // for: Optimism lists both USDT and USDT0, and they are different contracts.
  const candidates = [wanted, ...(LIFI_SYMBOL_ALIASES[chainId]?.[wanted] ?? [])];
  for (const candidate of candidates) {
    try {
      const token = await getLiFiToken(chainId, candidate);
      if (token) return token;
    } catch {
      /* try the next spelling, then the list */
    }
  }
  try {
    const tokens = await getLiFiTokens(chainId);
    for (const candidate of candidates) {
      const token = tokens.find(t => t.symbol?.toUpperCase() === candidate);
      if (token) return token;
    }
    return null;
  } catch {
    return null;
  }
}

// Other names LiFi lists a token under, per LiFi chain id. On Arbitrum, Tether
// exists only as USDT0 — the same contract (0xFd08…Cbb9) our wallet calls USDT
// — so a lookup for "USDT" there finds nothing (checked 2026-09-14). Keyed by
// chain because USDT0 elsewhere can be a different contract from USDT.
const LIFI_SYMBOL_ALIASES: Record<number, Record<string, string[]>> = {
  42161: { USDT: ['USDT0'] },
};

type QuoteStatus = 'idle' | 'quoting' | 'quoted' | 'no_route' | 'error' | 'expired';
type Screen = 'form' | 'confirm' | 'signing' | 'success' | 'failure';

export default function AllSwapScreen({ preselect }: AllSwapScreenProps) {
  const { colors, spacing, radius } = useTheme();
  const [fromToken, setFromToken] = useState<LiFiToken | null>(null);
  // Seeded from the preselection so the token-list, balance and picker effects
  // never fire once against a default chain the user is not on. An unresolvable
  // key (Tron, or anything unknown) keeps the default — see the effect below,
  // which is where that becomes a message instead of a wrong chain.
  const [fromChainId, setFromChainId] = useState(
    () => resolveLiFiChainId(preselect?.fromChain) ?? DEFAULT_FROM_CHAIN_ID,
  );
  const [toToken, setToToken] = useState<LiFiToken | null>(null);
  const [toChainId, setToChainId] = useState(
    () => resolveLiFiChainId(preselect?.toChain) ?? DEFAULT_TO_CHAIN_ID,
  );
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

  // Derived non-EVM addresses for the active wallet — the from/to address on
  // Solana-, Bitcoin- and Sui-side routes. Every family comes off the same
  // mnemonic, so a cross-VM swap needs no address input from the user.
  const [solanaAddress, setSolanaAddress] = useState<string | null>(null);
  const [btcAddress, setBtcAddress] = useState<string | null>(null);
  const [suiAddress, setSuiAddress] = useState<string | null>(null);
  // Whether derivation has finished. A null address means "still deriving"
  // before this flips and "this wallet has no key for that chain" after — a
  // key-only or watch-only wallet has no mnemonic to derive from, and telling
  // that user we are still preparing an address is a promise we never keep.
  const [addressesDerived, setAddressesDerived] = useState(false);

  useEffect(() => {
    getActiveWallet()
      .then(addr => { if (addr) setWalletAddress(addr); })
      .finally(() => setWalletLoading(false));
  }, []);

  // Legs the user has since chosen (or re-chained) themselves. A preselection
  // resolves over the network, so its answer can land AFTER a tap that already
  // picked something else — applying it then silently undoes the user's choice.
  const userChoseLeg = useRef({ from: false, to: false });
  // Progress of the preselection, deliberately NOT a blocking loading state:
  // the form renders and accepts input throughout. A token list can be slow,
  // or never answer, and a screen that waits on one is a spinner with no way
  // out — the user must always be left on a form they can finish by hand.
  const [preselecting, setPreselecting] = useState(false);
  const [preselectNotice, setPreselectNotice] = useState('');

  // Tracks `screen` for the preselection effect, which must consult it without
  // taking a dependency on it.
  const screenRef = useRef(screen);
  screenRef.current = screen;

  const wantFromSymbol = preselect?.fromSymbol;
  const wantFromChain = preselect?.fromChain;
  const wantToSymbol = preselect?.toSymbol;
  const wantToChain = preselect?.toChain;
  const wantFundedLeg = preselect?.fundedLeg;

  useEffect(() => {
    // Nothing asked for: the screen starts on its defaults, as it always has.
    if (!wantFromSymbol && !wantToSymbol) return;
    // Never disturb a swap that is already being signed. The tab stays mounted,
    // so a listing tapped elsewhere pushes new params into THIS screen
    // mid-flight. Read through a ref, not the state: depending on `screen`
    // would re-run this effect on every screen change and wipe the form on the
    // way to confirm.
    if (screenRef.current === 'signing') return;
    let cancelled = false;

    // A new pair invalidates everything priced against the old one, exactly as
    // picking a token by hand does (see onSelect). Without this the tokens
    // change while `quote` survives; validate() only checks that A quote
    // exists, not that it matches these legs, so Review stays enabled and
    // handleSwap signs the PREVIOUS pair's route under the new pair's name.
    setFromAmount(''); setQuote(null); setQuoteStatus('idle'); setQuoteError('');
    // Back to the form: the confirm card renders whatever tokens are current,
    // so leaving it up would show the new pair over the old quote's terms.
    setScreen('form');
    // A different pair in the params is a new instruction — from a listing the
    // user just tapped — so it outranks the legs they chose for the last one.
    // This effect only re-runs when those params actually change, which is why
    // clearing here cannot undo a choice made against the CURRENT request.
    userChoseLeg.current = { from: false, to: false };
    const legs = [
      {
        side: 'from' as const, symbol: wantFromSymbol, chainKey: wantFromChain,
        chainId: resolveLiFiChainId(wantFromChain),
        setChainId: setFromChainId, setToken: setFromToken,
      },
      {
        side: 'to' as const, symbol: wantToSymbol, chainKey: wantToChain,
        chainId: resolveLiFiChainId(wantToChain),
        setChainId: setToChainId, setToken: setToToken,
      },
    ].filter((leg): leg is typeof leg & { symbol: string } => !!leg.symbol);

    // Chains move first and synchronously. The per-chain token lists, the
    // balance read and the picker all key off them, so resolving symbols while
    // they still point at the defaults would fetch the wrong chain's data and
    // briefly offer the user the wrong chain's tokens.
    // The token is cleared with the chain, not left for the resolver to
    // overwrite: if the new leg never resolves, holding the PREVIOUS token
    // against the NEW chain quotes a different asset under the old symbol,
    // beneath a notice implying the leg is empty.
    //
    // A funded leg is the exception: its chain is not known until the wallet
    // read answers, and moving it to the fallback first would fetch that
    // chain's multi-MB token list only to throw it away.
    legs.forEach((leg) => {
      if (leg.chainId != null && leg.side !== wantFundedLeg) leg.setChainId(leg.chainId);
      leg.setToken(null);
    });

    setPreselectNotice('');
    setPreselecting(true);
    (async () => {
      // Each leg applies the moment its own lookup lands. Waiting for both
      // would let one chain's slow (or dead) token list hold the other leg
      // unselected, which is the failure this whole effect exists to avoid.
      const unresolved = (await Promise.all(legs.map(async (leg) => {
        // A funded leg moves once the wallet read says where the user actually
        // holds the token; that read falls back to the leg's own chain.
        let chainId = leg.chainId;
        if (leg.side === wantFundedLeg) {
          const funded = resolveLiFiChainId(await fundedQuoteChain(leg.symbol, leg.chainKey));
          if (cancelled) return null;
          if (funded != null && !userChoseLeg.current[leg.side]) {
            chainId = funded;
            leg.setChainId(funded);
          }
        }
        const token = chainId == null ? null : await resolveLiFiSymbol(chainId, leg.symbol);
        if (cancelled) return null;
        // Checked again AFTER the await, not only before it: the tap that makes
        // a preselection stale is most likely to land while it is in flight.
        if (token && !userChoseLeg.current[leg.side]) leg.setToken(token);
        return token ? null : leg.symbol.toUpperCase();
      }))).filter((symbol): symbol is string => symbol !== null);
      if (cancelled) return;
      setPreselecting(false);
      // An unresolvable leg is the end of the preselection, not a retry loop:
      // LiFi may have dropped the token, or the request may have failed, and
      // either way the user is already on a form that can finish the job.
      setPreselectNotice(unresolved.length === 0 ? '' : `Couldn’t preselect ${
        unresolved.join(' and ')} — pick ${unresolved.length > 1 ? 'them' : 'it'} below to continue.`);
    })();
    return () => { cancelled = true; };
  }, [wantFromSymbol, wantFromChain, wantToSymbol, wantToChain, wantFundedLeg]);

  useEffect(() => {
    if (!walletAddress) return;
    let cancelled = false;
    setAddressesDerived(false);
    Promise.allSettled([
      getSolanaAddress(walletAddress), getBtcAddress(walletAddress), getSuiAddress(walletAddress),
    ]).then(([sol, btc, sui]) => {
      if (cancelled) return;
      setSolanaAddress(sol.status === 'fulfilled' ? sol.value : null);
      setBtcAddress(btc.status === 'fulfilled' ? btc.value : null);
      setSuiAddress(sui.status === 'fulfilled' ? sui.value : null);
      setAddressesDerived(true);
    });
    return () => { cancelled = true; };
  }, [walletAddress]);

  /**
   * The address that signs (and receives) on a given LiFi chain. Each VM family
   * derives its own key from the one mnemonic, so this is the single place the
   * screen decides "whose address is this route's side". null while a non-EVM
   * address is still deriving, or when the wallet has no mnemonic to derive it —
   * `addressesDerived` tells those two apart.
   */
  const addressForChain = useCallback((chainId: number): string | null => {
    if (isSolanaLiFiChain(chainId)) return solanaAddress;
    if (isBtcLiFiChain(chainId)) return btcAddress;
    if (isSuiLiFiChain(chainId)) return suiAddress;
    return walletAddress || null;
  }, [walletAddress, solanaAddress, btcAddress, suiAddress]);

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
  }, [fromChainId, solanaAddress, btcAddress, suiAddress]));

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
      // /wallet/balances is EVM-only — it 400s on the Solana/Bitcoin pseudo chain-id
      // and would leave the previous EVM chain's rows in place, which the
      // picker's "your tokens" section then matches BY SYMBOL against SPL
      // tokens. Read the SPL rows instead (or clear while the address derives).
      if (isSolanaLiFiChain(fromChainId)) {
        if (!solanaAddress) { setBalances([]); return; }
        setBalances(await getSolanaBalances(solanaAddress));
        return;
      }
      if (isBtcLiFiChain(fromChainId)) {
        if (!btcAddress) { setBalances([]); return; }
        setBalances(await getBtcBalances(btcAddress));
        return;
      }
      if (isSuiLiFiChain(fromChainId)) {
        if (!suiAddress) { setBalances([]); return; }
        setBalances(await getSuiBalances(suiAddress));
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
        if (isBtcLiFiChain(fromChainId)) {
          if (!btcAddress) return;
          // Bitcoin carries exactly one asset, so there is no token row to
          // match — the native balance IS the from-token balance.
          const rows = await getBtcBalances(btcAddress);
          if (cancelled) return;
          setLiveFromBalance(rows[0]?.balance ?? '0');
          return;
        }
        if (isSuiLiFiChain(fromChainId)) {
          if (!suiAddress) return;
          // Sui carries many coin types, so match the selected one by symbol
          // against the wallet's rows rather than assuming the native coin.
          const rows = await getSuiBalances(suiAddress);
          if (cancelled) return;
          const row = rows.find(b => b.symbol.toUpperCase() === fromToken.symbol.toUpperCase());
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
  }, [fromToken, fromChainId, walletAddress, solanaAddress, btcAddress, suiAddress, refreshKey]);

  useEffect(() => {
    if (timer.current) clearTimeout(timer.current);
    if (!fromToken || !toToken || !fromAmount || isNaN(Number(fromAmount)) || Number(fromAmount) <= 0 || !walletAddress) {
      setQuote(null); setQuoteStatus('idle'); setQuoteError('');
      return;
    }
    // Each side quotes against the address of its own VM family; skip until
    // both have derived (near-instant once the wallet loads).
    const fromAddress = addressForChain(fromChainId);
    const toAddress = addressForChain(toChainId);
    if (!fromAddress || !toAddress) {
      setQuote(null); setQuoteStatus('idle'); setQuoteError('');
      return;
    }
    setQuoteStatus('quoting');
    timer.current = setTimeout(async () => {
      try {
        const amountWei = ethers.parseUnits(fromAmount, fromToken.decimals).toString();
        // Cross-VM routes (EVM↔Solana, EVM↔Bitcoin, Solana↔Bitcoin) must name
        // the destination-chain recipient explicitly — the sending address
        // cannot receive there. Both sides derive from the same mnemonic, so no
        // user input is needed.
        const q = await getLiFiQuote({
          fromChain: fromChainId,
          toChain: toChainId,
          fromToken: fromToken.address,
          toToken: toToken.address,
          fromAmount: amountWei,
          fromAddress,
          ...(toAddress !== fromAddress ? { toAddress } : {}),
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
  }, [fromToken, toToken, fromAmount, fromChainId, toChainId, walletAddress, addressForChain, slippageAuto, slippage, refreshKey]);

  const swapDirection = () => {
    // Both legs are now the user's doing, so an in-flight preselection must
    // not land on either of them.
    userChoseLeg.current = { from: true, to: true };
    const [nf, nfc, nt, ntc] = [toToken, toChainId, fromToken, fromChainId];
    setFromToken(nf); setFromChainId(nfc);
    setToToken(nt); setToChainId(ntc);
    setFromAmount(''); setQuote(null); setQuoteStatus('idle');
  };

  // `balances` holds EVM rows from /wallet/balances — never let a non-EVM
  // from-token fall through to them by symbol (EVM USDC ≠ SPL USDC, and an
  // EVM-wrapped BTC ≠ real BTC).
  const fromBalance =
    liveFromBalance ??
    (isNonEVMLiFiChain(fromChainId)
      ? '0'
      : balances.find(b => b.symbol === fromToken?.symbol)?.balance ?? '0');

  const validate = () => {
    if (!fromToken) return 'Select a token to send';
    if (!toToken) return 'Select a token to receive';
    if (!fromAmount || Number(fromAmount) <= 0) return 'Enter an amount';
    // A non-EVM side needs its own derived address before LiFi can be quoted;
    // say so rather than sit on "Waiting for price…" forever.
    const addressNotice = missingAddressNotice([
      { chainName: chainName(fromChainId), address: addressForChain(fromChainId) },
      { chainName: chainName(toChainId), address: addressForChain(toChainId) },
    ], addressesDerived);
    if (addressNotice) return addressNotice;
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
    if (!quote || !fromToken || !toToken || !walletAddress) return;
    setScreen('signing'); setError('');

    // Built BEFORE the swap runs, so that nothing about reporting the fee can
    // throw after the swap is on-chain — a completed swap must never land on
    // the failure screen. The backend also records the fee as owed at submit
    // time, so this call is the fast path, not the only one.
    const feeReport = {
      fromChainId,
      toChainId,
      fromToken: fromToken.address,
      toToken: toToken.address,
      fromAmount: ethers.parseUnits(fromAmount, fromToken.decimals).toString(),
      toAmount: quote.toAmount,
      fromSymbol: fromToken.symbol,
      fromDecimals: fromToken.decimals,
      // The same endpoints the quote used — LiFi will not re-quote without
      // them, and a UTXO route needs the funded sender specifically.
      fromAddress: addressForChain(fromChainId) ?? walletAddress,
      toAddress: addressForChain(toChainId) ?? walletAddress,
      feature: 'allswap' as const,
    };

    let swapHash: string;
    try {
      swapHash = await executeLiFiSwap({
        quote,
        fromSymbol: fromToken.symbol,
        fromDecimals: fromToken.decimals,
        fromTokenAddress: fromToken.address,
        fromAmount,
        walletAddress,
        fromChainId,
        onStage: setSigningStatus,
      });
    } catch (e: any) {
      console.error('[swap] handleSwap failed', e);
      setError(e?.message || 'Swap failed'); setScreen('failure');
      return;
    }

    setTxHash(swapHash); setScreen('success');
    settleSwapFee({ ...feeReport, quoteId: swapHash })
      .catch(e => console.error('[swap] fee settle failed', { txHash: swapHash, e }));
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
    if (isBtcLiFiChain(chainId)) return NONEVM_CHAINS.btc.shortName;
    if (isSuiLiFiChain(chainId)) return NONEVM_CHAINS.sui.shortName;
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
      crossChain={fromChainId !== toChainId}
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
        <ScreenHeader
          onBack={() => setScreen('form')}
          style={{ paddingHorizontal: 0, marginBottom: spacing.lg }}
        />
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>Confirm Swap</Text>

        {/* From → To summary */}
        <Card padding="lg">
          <View style={{ paddingVertical: spacing.md }}>
            <Text variant="overline" color="textMuted">YOU PAY</Text>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, marginTop: spacing.sm }}>
              {fromToken ? <TokenAvatar symbol={fromToken.symbol} iconUrl={fromToken.logoURI} curated={isKnownContract(fromToken.chainId, fromToken.address, fromToken.symbol)} size={40} /> : null}
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
              {toToken ? <TokenAvatar symbol={toToken.symbol} iconUrl={toToken.logoURI} curated={isKnownContract(toToken.chainId, toToken.address, toToken.symbol)} size={40} /> : null}
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
          {quote?.feeCostUSD ? <DetailRow label="Fee" value={`$${quote.feeCostUSD}`} highlight /> : null}
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
                accessibilityLabel="Select token to pay"
              >
                {fromToken ? <TokenAvatar symbol={fromToken.symbol} iconUrl={fromToken.logoURI} curated={isKnownContract(fromToken.chainId, fromToken.address, fromToken.symbol)} size={24} /> : null}
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
                accessibilityLabel="Select token to receive"
              >
                {toToken ? <TokenAvatar symbol={toToken.symbol} iconUrl={toToken.logoURI} curated={isKnownContract(toToken.chainId, toToken.address, toToken.symbol)} size={24} /> : null}
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

          {/* Preselection progress. Rendered ABOVE the form's own messages and
              never in place of the form: a listing that cannot be preselected
              still leaves a usable swap. */}
          {preselecting && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, marginTop: spacing.md }}>
              <ActivityIndicator size="small" color={colors.textMuted} />
              <Text variant="caption" color="textMuted" style={{ flex: 1 }}>Selecting this pair…</Text>
            </View>
          )}
          {!preselecting && !!preselectNotice && (
            <View
              style={{
                flexDirection: 'row', alignItems: 'center', gap: spacing.xs,
                backgroundColor: colors.warningBg, borderWidth: 1, borderColor: colors.warning,
                borderRadius: radius.md, padding: spacing.sm, marginTop: spacing.md,
              }}
            >
              <Icon name="warning" size={13} color={colors.warning} />
              <Text variant="caption" style={{ flex: 1, color: colors.warning }}>{preselectNotice}</Text>
            </View>
          )}

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
                {quote.tool} · ~{Math.ceil(quote.estimatedDuration / 60)} min
                {quote.feeCostUSD ? ` · $${quote.feeCostUSD} fee` : ''} · ${quote.gasCostUSD} gas
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
          // Stays mounted while closed on purpose: the picker fetches the LiFi
          // chain list once per mount and clears its own search/chain/paging
          // state off `visible`, so unmounting it on close would refetch that
          // whole list on every open. The fallback exists only to satisfy the
          // non-nullable prop on that closed render, and says nothing about a
          // leg being live — `visible` is the only signal that does.
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
            // No mode means no leg was ever opened, and an untaken `from`
            // branch here re-chains `to` — the leg the user was not touching.
            if (pickerMode === null) return;
            // Changing a chain is choosing a leg: whatever the preselection is
            // still fetching for this side is now the wrong chain's token.
            userChoseLeg.current[pickerMode] = true;
            if (pickerMode === 'from') setFromChainId(newChainId);
            else setToChainId(newChainId);
          }}
          onSelect={(token, chainId) => {
            // Same absent-leg guard, and it protects more here: a fall-through
            // would not just write `to`, it would also drop the preselect
            // notice and wipe an amount the user has already typed.
            if (pickerMode === null) return;
            userChoseLeg.current[pickerMode] = true;
            setPreselectNotice('');
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

function SwapSuccessScreen({ fromToken, toToken, fromAmount, outputAmount, txHash, chainId, crossChain, onDone }: {
  fromToken: LiFiToken | null; toToken: LiFiToken | null;
  fromAmount: string; outputAmount: string | null;
  txHash: string; chainId: number; crossChain: boolean; onDone: () => void;
}) {
  const { colors, spacing, radius } = useTheme();

  // A same-chain swap is done when its transaction confirms. A cross-chain one
  // is not: the bridge still has to deliver on the far side, so saying
  // "complete" at broadcast tells the user their funds have arrived when they
  // are still in flight — and says nothing at all when the bridge fails.
  const [delivery, setDelivery] = useState<SwapDeliveryStatus>(crossChain ? 'PENDING' : 'DONE');
  const [gaveUpWaiting, setGaveUpWaiting] = useState(false);
  useEffect(() => {
    if (!crossChain || !txHash) return;
    let cancelled = false;
    let timer: ReturnType<typeof setTimeout>;
    // A bridge leg runs to minutes, so keep asking — but not forever. Past this
    // the screen stops polling and points at the explorer rather than holding a
    // spinner (and a LiFi call every 15s) open indefinitely.
    let attempts = 0;
    const MAX_ATTEMPTS = 40; // ~10 minutes
    const poll = async () => {
      const { status } = await getSwapDelivery(txHash);
      if (cancelled) return;
      setDelivery(status);
      if (status === 'PENDING' && ++attempts < MAX_ATTEMPTS) {
        timer = setTimeout(poll, 15_000);
      } else if (status === 'PENDING') {
        setGaveUpWaiting(true);
      }
    };
    void poll();
    return () => { cancelled = true; clearTimeout(timer); };
  }, [crossChain, txHash]);

  const title =
    delivery === 'DONE' ? 'Swap Complete'
      : delivery === 'FAILED' ? 'Bridge Failed'
        : 'Swap Sent';
  const subtitle =
    delivery === 'FAILED' ? 'The bridge could not complete this route. Bridges refund to your address when they fail — check the explorer link below.'
      : delivery !== 'PENDING' ? null
        : gaveUpWaiting ? 'Still bridging. This one is taking longer than usual — follow the explorer link below for the latest.'
          : 'Bridging to the destination chain — this can take a few minutes. You can close this screen; it will keep going.';
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
          <Text variant="title1" weight="700" align="center" style={{ marginBottom: subtitle ? spacing.sm : spacing['2xl'] }}>{title}</Text>
          {subtitle ? (
            <Text variant="caption" color="textMuted" align="center" style={{ marginBottom: spacing['2xl'] }}>
              {subtitle}
            </Text>
          ) : null}

          <Card padding="lg" style={{ width: '100%' }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.md }}>
              {fromToken ? <TokenAvatar symbol={fromToken.symbol} iconUrl={fromToken.logoURI} curated={isKnownContract(fromToken.chainId, fromToken.address, fromToken.symbol)} size={36} /> : null}
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
              {toToken ? <TokenAvatar symbol={toToken.symbol} iconUrl={toToken.logoURI} curated={isKnownContract(toToken.chainId, toToken.address, toToken.symbol)} size={36} /> : null}
              <View style={{ flex: 1 }}>
                <Text variant="title3" weight="700" color="accent">{outputAmount} {toToken?.symbol}</Text>
                <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>{toToken?.name}</Text>
              </View>
            </View>
          </Card>

          {txHash ? (() => {
            const url = isNonEVMLiFiChain(chainId)
              ? txExplorerUrl(txHash, {
                  chainKey: isSolanaLiFiChain(chainId) ? 'solana' : isSuiLiFiChain(chainId) ? 'sui' : 'btc',
                }) ?? null
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
