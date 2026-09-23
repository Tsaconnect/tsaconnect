// app/wallet/paycrest/sell.tsx
//
// Sell stablecoins for local fiat (the selected ramp currency) through
// Paycrest's provider network.
//
// Non-custodial: the user sends the stablecoin FROM their own connected wallet
// to the provider's address, and the provider pays their bank directly. TSA
// never holds the asset or the fiat — which also means we cannot reverse a
// send, so the deposit address and amount are shown copyably and never
// truncated.
//
// The user types the fiat they want to receive; the token amount is derived
// from the live quote. That is the right way round for this market — people
// think in "I need ₦50,000", not "I'll sell 36.4 USDC".
//
// Flow: amount -> live routes -> payout bank -> create -> send from the app
// (or, for funds held elsewhere, the deposit address) -> the order screen.
//
// Funding is checked BEFORE the order is created, not at the send: an order
// carries a countdown, and starting one against a route the wallet cannot
// cover burns that window on a sale that was never going to complete. See
// services/paycrestDeposit.ts#checkFunding.
//
// Any other coin she holds (design §5) is a SWAPPED sell: she picks it from
// her holdings, types how much of it, sees one "~₦" estimate and taps Sell
// once. useSwappedSell does the rest — swap to USDC on Base in her own wallet,
// open the payout for what landed, send it — and "Finish your sale" picks up
// a sale that stopped part-way.
import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
import { ethers } from 'ethers';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router, useLocalSearchParams } from 'expo-router';
import { useTheme } from '@/theme';
import {
  AmountInput,
  Button,
  Card,
  CopyableField,
  Input,
  PickerSheet,
  SelectRow,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import { DepositProgress } from '@/components/paycrest/DepositProgress';
import AssetPicker from '@/components/assets/AssetPicker';
import { canCancelSale, useSwappedSell, type SellStage } from '@/hooks/useSwappedSell';
import { useWalletHoldings } from '@/hooks/useWalletHoldings';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import { rampCurrencyLabel, useRampCurrency } from '@/hooks/useRampCurrency';
import { useScreenTitle } from '@/hooks/useScreenTitle';
import { resolveAccountName } from '@/services/ngBanks';
import {
  accountFieldLabel,
  accountFieldPlaceholder,
  accountValueSurvivesKindChange,
  accountNumberMaxLength,
  isDigitsOnlyAccountField,
  isValidAccountNumber,
  sanitizeAccountNumberInput,
} from '@/utils/payoutAccount';
import {
  cancelOrder,
  claimPayment,
  createSell,
  getPayoutInstitutions,
  getQuote,
  PaycrestError,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type PayoutInstitution,
} from '@/services/paycrestApi';
import {
  balanceOf,
  checkFunding,
  sendSponsoredPaycrestDeposit,
  type FundingCheck,
} from '@/services/paycrestDeposit';
import {
  getSponsoredRoutes,
  getSponsorshipOffer,
  isGasSponsored,
  type SponsorRoute,
} from '@/services/gasSponsorship';
import {
  getWalletBalances,
  normalizeWalletBalances,
  type WalletBalanceWithChain,
} from '@/services/walletApi';
import {
  getSellSwapQuote,
  getSwappedSellState,
  type SellIntent,
  type SellSwapQuote,
} from '@/services/paycrestSellSwap';
import {
  isBtcLiFiChain,
  isSolanaLiFiChain,
  isSuiLiFiChain,
  SOLANA_LIFI_CHAIN_ID,
  type LiFiToken,
} from '@/services/lifi';
import { isNativeLiFiToken } from '@/services/assetCatalog';
import { getSolanaAddress } from '@/services/chains/solanaWallet';
import { getBtcAddress } from '@/services/chains/btcWallet';
import { getSuiAddress } from '@/services/chains/suiWallet';
import { nativeGasReserve } from '@/services/wallet';
import { SOLANA_SWAP_RESERVE_LAMPORTS, spendableNative } from '@/services/gasReserve';
import { CHAINS, getChainByChainId, type ChainKey } from '@/constants/chains';

const QUOTE_DEBOUNCE_MS = 450;
/** Warn while the user can still act. An on-chain send needs gas, a signature
 *  and confirmations — more steps than a bank transfer, so warn earlier. */
const EXPIRY_WARNING_SECONDS = 420;

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

const venueId = (v: PaycrestVenue) => `${v.token}:${v.chain}`;
const venueLabel = (v: PaycrestVenue) => `${v.token} on ${CHAIN_LABELS[v.chain] ?? v.chain}`;
const fmtTokenAmount = (n: number) =>
  n.toLocaleString(undefined, { maximumFractionDigits: n >= 1 ? 2 : 6 });

/** The trailing half of a route's sublabel — silent when the route is fine. */
function routeNote(check: FundingCheck): string {
  if (check.ok) return '';
  if (check.reason === 'short-token') return ` · you have ${check.have}`;
  if (check.reason === 'no-gas') return ` · no ${check.nativeSymbol} for gas`;
  return ' · not supported in-app';
}

/** The wallet's gas balance on a chain, or null when balances aren't known. */
function nativeBalanceFor(chain: string, balances: WalletBalanceWithChain[] | null): number | null {
  const symbol = CHAINS[chain as ChainKey]?.nativeCurrency.symbol;
  if (!symbol || !balances) return null;
  const row = balances.find((b) => b.chainKey === chain && b.symbol === symbol);
  const n = Number(row?.balance ?? 0);
  return Number.isFinite(n) ? n : 0;
}

/** A coin Paycrest cannot take, sold by swapping it to USDC on Base first. */
type SwapPick = { token: LiFiToken; chainId: number; sourceAddress: string };

/** Stablecoins this screen already sells directly — only consulted before
 *  any quote has said which routes are live. */
const DIRECT_TOKENS = new Set(['USDC', 'USDT']);

/** Her address on the chain she is selling from — the EVM wallet itself, or
 *  the Solana/Bitcoin/Sui address derived from the same key. */
async function sourceAddressFor(chainId: number, evmAddress: string): Promise<string | null> {
  if (isSolanaLiFiChain(chainId)) return getSolanaAddress(evmAddress);
  if (isBtcLiFiChain(chainId)) return getBtcAddress(evmAddress);
  if (isSuiLiFiChain(chainId)) return getSuiAddress(evmAddress);
  return evmAddress;
}

function networkName(chainId: number): string {
  if (isSolanaLiFiChain(chainId)) return 'Solana';
  if (isBtcLiFiChain(chainId)) return 'Bitcoin';
  if (isSuiLiFiChain(chainId)) return 'Sui';
  return getChainByChainId(chainId)?.name ?? 'another network';
}

/** A float balance as a decimal string, rounded DOWN so MAX never asks for
 *  more than she holds. */
function floorBalance(balance: number, decimals: number): string {
  const places = Math.min(decimals, 8);
  const scale = 10 ** places;
  return (Math.floor(balance * scale) / scale).toFixed(places);
}

const SWAPPED_STAGE_LABELS: Partial<Record<SellStage, string>> = {
  swapping: 'Swapping your coins…',
  waiting: 'Waiting for your swap to arrive — this can take a few minutes…',
  ordering: 'Opening your payout…',
  sending: 'Sending for payout…',
};

/** The same verdict as a full sentence, for the form. */
function fundingMessage(check: FundingCheck): string | null {
  if (check.ok) return null;
  const chain = CHAIN_LABELS[check.chain] ?? check.chain;
  if (check.reason === 'short-token') {
    return `You have ${check.have} ${check.token} on ${chain} — this sale needs ${check.need}. Pick another network or top up.`;
  }
  if (check.reason === 'no-gas') {
    return `You have no ${check.nativeSymbol} on ${chain} to pay the network fee. Pick another network or add some ${check.nativeSymbol}.`;
  }
  return `This app cannot send on ${chain} yet — choose another network, or send from an external wallet.`;
}

export default function PaycrestSellScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  const { asset, currency: currencyParam } = useLocalSearchParams<{
    asset?: string;
    currency?: string;
  }>();

  // The currency this order settles in — NOT the display currency. See
  // hooks/useRampCurrency.ts: deriving this from CurrencyContext's display
  // preference is the exact regression documented at wallet/it/index.tsx (a
  // user who switched display to USD lost the working NGN ramp). Restricted
  // to 'paycrest' so the picker can never offer a currency this screen would
  // fail to quote.
  const {
    code: currencyCode,
    currency: currencyConfig,
    setCode: setCurrencyCode,
    options: currencyOptions,
  } = useRampCurrency('paycrest', 'sell');
  const symbol = currencyConfig.symbol;
  const currencyLabel = rampCurrencyLabel(currencyCode);
  useScreenTitle(`Sell for ${currencyLabel}`);
  useEffect(() => {
    if (currencyParam) setCurrencyCode(currencyParam);
    // One-time sync of the hub's choice on arrival — see the identical note
    // on the buy screen.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currencyParam]);
  const [currencyPickerOpen, setCurrencyPickerOpen] = useState(false);

  // resolveAccountName is a Nigerian NUBAN lookup — it cannot resolve an
  // account in any other currency's country, so gate both the lookup and the
  // submit requirement on NGN or sell becomes permanently stuck outside it.
  const requiresNameCheck = currencyCode === 'NGN';

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  const [routeOpen, setRouteOpen] = useState(false);

  // Payout destination.
  const [banks, setBanks] = useState<PayoutInstitution[]>([]);
  const [bank, setBank] = useState<PayoutInstitution | null>(null);
  const [bankOpen, setBankOpen] = useState(false);
  // What the chosen destination actually is. Paycrest reports it per
  // institution — every UGX option is mobile money, KES and TZS carry a few
  // among their banks — and it decides what the account field asks for. An
  // API build that does not send it reads as a bank, the majority everywhere.
  const destKind = bank?.type ?? 'bank';
  const [accountNo, setAccountNo] = useState('');
  const [accountName, setAccountName] = useState('');
  const [resolving, setResolving] = useState(false);

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [creating, setCreating] = useState(false);

  // The in-app send.
  const [sending, setSending] = useState(false);
  const [sendError, setSendError] = useState<string | null>(null);
  const [manualOpen, setManualOpen] = useState(false);
  const [cancelling, setCancelling] = useState(false);

  // Every chain's balances in one request — the un-filtered endpoint already
  // fans out server-side, so annotating all five routes costs no more than
  // checking one. null means "not known", which never blocks anything.
  const [balances, setBalances] = useState<WalletBalanceWithChain[] | null>(null);

  const quoteSeq = useRef(0);

  // ── What she is selling ────────────────────────────────────────────────
  // Nothing picked, or a stablecoin a live route takes: today's direct sale.
  // Anything else she holds: the swapped sale.
  const [pickerOpen, setPickerOpen] = useState(false);
  const [directPick, setDirectPick] = useState<{ chain: string; token: string } | null>(null);
  const [swapPick, setSwapPick] = useState<SwapPick | null>(null);
  const [swapAmount, setSwapAmount] = useState('');
  const [swapQuote, setSwapQuote] = useState<SellSwapQuote | null>(null);
  const [swapQuoting, setSwapQuoting] = useState(false);
  const [swapQuoteError, setSwapQuoteError] = useState<string | null>(null);
  const swapQuoteSeq = useRef(0);
  const swapped = useSwappedSell();
  const swappedBusy = !!SWAPPED_STAGE_LABELS[swapped.stage];
  const { holdings, loading: holdingsLoading } = useWalletHoldings(!!swapPick);
  const [openIntent, setOpenIntent] = useState<SellIntent | null>(null);
  // Whether this server sells any token by swapping it first. Off until the
  // server says so: with RAMP_CONVERSION_SELL unset every non-stable pick
  // would fail at its quote, so the screen stays the stablecoin sale it was.
  const [swappedSellOn, setSwappedSellOn] = useState(false);
  const [cancellingSale, setCancellingSale] = useState(false);

  // An offramp expires exactly like an onramp does. Sending after the window
  // closes puts crypto at a provider address for an order that no longer
  // exists — the same stranded-funds failure as a late bank transfer, and just
  // as unrecoverable, so the deadline has to be on screen.
  const { label: countdown, expired, secs } = useCountdown(order?.validUntil);
  const expiringSoon = !expired && secs > 0 && secs <= EXPIRY_WARNING_SECONDS;

  // Balances for the signable wallet, fetched once it is known. A failure
  // leaves `balances` null and every funding check answers "ok" — see
  // checkFunding: a flaky indexer must not block a sale the user can fund.
  useEffect(() => {
    if (!address) return;
    let cancelled = false;
    getWalletBalances(undefined, address)
      .then((res) => {
        if (cancelled) return;
        setBalances(res.success ? normalizeWalletBalances(res.data) : null);
      })
      .catch(() => {
        if (!cancelled) setBalances(null);
      });
    return () => {
      cancelled = true;
    };
  }, [address]);

  // Routes this user sells on without holding gas. An empty list — sponsorship
  // off, an older server, a network error — keeps today's gas warning.
  const [sponsoredRoutes, setSponsoredRoutes] = useState<SponsorRoute[]>([]);
  useEffect(() => {
    let cancelled = false;
    getSponsoredRoutes().then((routes) => {
      if (!cancelled) setSponsoredRoutes(routes);
    });
    return () => {
      cancelled = true;
    };
  }, []);

  // The routes list is a pre-order hint; the per-order offer can still come
  // back 'none' (domain unverified, too little time left, lane switched off),
  // and then the send self-pays. Only claim the fee is covered when it is.
  const [orderSponsored, setOrderSponsored] = useState(false);
  useEffect(() => {
    if (!order?.id) {
      setOrderSponsored(false);
      return;
    }
    let cancelled = false;
    getSponsorshipOffer(order.id)
      .then((offer) => {
        if (!cancelled) setOrderSponsored(offer.mode !== 'none');
      })
      .catch(() => {
        if (!cancelled) setOrderSponsored(false);
      });
    return () => {
      cancelled = true;
    };
  }, [order?.id]);

  // The server returns the list already in the right vocabulary for this
  // currency, so there is nothing to intersect. Outside Nigeria there is no
  // local list to intersect WITH. The previously selected bank is cleared on
  // a currency change — it belongs to the old currency's list and may not
  // even exist in the new one.
  useEffect(() => {
    setBank(null);
    getPayoutInstitutions(currencyCode)
      .then(setBanks)
      .catch(() => setBanks([]));
  }, [currencyCode]);

  // Live quoting, same contract as the buy screen: a quote is only true for
  // the size it was asked about, and only for a moment.
  useEffect(() => {
    const raw = amount.trim();
    if (!raw || Number(raw) <= 0) {
      setQuote(null);
      setVenue(null);
      setQuoteError(null);
      return;
    }
    const seq = ++quoteSeq.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      try {
        const q = await getQuote('sell', raw, currencyCode);
        if (seq !== quoteSeq.current) return;
        setQuote(q);
        setQuoteError(null);
        setVenue((prev) => {
          const kept = prev && q.venues.find((v) => venueId(v) === venueId(prev));
          const picked = directPick
            ? q.venues.find((v) => v.chain === directPick.chain && v.token.toUpperCase() === directPick.token)
            : undefined;
          const preferred = asset
            ? q.venues.find((v) => v.token.toUpperCase() === asset.toUpperCase())
            : undefined;
          return kept ?? picked ?? preferred ?? q.best ?? null;
        });
      } catch (e) {
        if (seq !== quoteSeq.current) return;
        setQuote(null);
        setVenue(null);
        setQuoteError(
          e instanceof PaycrestError ? e.message : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === quoteSeq.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [amount, asset, currencyCode, directPick]);

  // ── The swapped sale ───────────────────────────────────────────────────

  // Her unfinished sale, if any, so "Finish your sale" can pick it up — and
  // whether swapped sells exist at all: a 404 means the routes are not
  // registered, so no swapped-sell UI is shown. Any other failure proves
  // nothing either way and leaves the last answer standing.
  const refreshOpenIntent = useCallback(() => {
    getSwappedSellState()
      .then(({ enabled, intent }) => {
        setSwappedSellOn(enabled);
        setOpenIntent(intent);
      })
      .catch((e) => {
        // A server that answered at all has the routes; only 404 says it doesn't.
        if (e instanceof PaycrestError) setSwappedSellOn(true);
        setOpenIntent(null);
      });
  }, []);
  useEffect(() => {
    if (address) refreshOpenIntent();
  }, [address, refreshOpenIntent]);

  // The one quote a swapped sale shows: "~₦X", debounced like the direct one.
  useEffect(() => {
    const raw = swapAmount.trim();
    if (!swapPick || !address || !raw || !(Number(raw) > 0)) {
      swapQuoteSeq.current++;
      setSwapQuote(null);
      setSwapQuoteError(null);
      setSwapQuoting(false);
      return;
    }
    let fromAmount: string;
    try {
      fromAmount = ethers.parseUnits(raw, swapPick.token.decimals).toString();
    } catch {
      setSwapQuote(null);
      setSwapQuoteError('Enter a valid amount.');
      return;
    }
    const seq = ++swapQuoteSeq.current;
    setSwapQuoting(true);
    const t = setTimeout(async () => {
      try {
        const q = await getSellSwapQuote({
          fromChainId: swapPick.chainId,
          fromToken: swapPick.token.address,
          fromAmount,
          fromAddress: swapPick.sourceAddress,
          walletAddress: address,
          currency: currencyCode,
        });
        if (seq !== swapQuoteSeq.current) return;
        setSwapQuote(q);
        setSwapQuoteError(null);
      } catch (e) {
        if (seq !== swapQuoteSeq.current) return;
        setSwapQuote(null);
        setSwapQuoteError(
          e instanceof PaycrestError && e.status === 422
            ? "This token can't be sold for naira right now"
            : e instanceof PaycrestError
              ? e.message
              : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === swapQuoteSeq.current) setSwapQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [swapPick, swapAmount, address, currencyCode]);

  const onPick = useCallback(
    async (token: LiFiToken, chainId: number) => {
      if (!address) return;
      const chain = getChainByChainId(chainId)?.key;
      const sym = token.symbol.toUpperCase();
      // A route Paycrest fills directly stays today's sale. Before anything is
      // typed there is no quote to ask, so the stablecoins it sells stand in.
      const direct = chain
        ? quote?.venues.length
          ? quote.venues.find((v) => v.chain === chain && v.token.toUpperCase() === sym)
          : DIRECT_TOKENS.has(sym) && chain in CHAIN_LABELS
            ? true
            : undefined
        : undefined;
      if (chain && direct) {
        setSwapPick(null);
        setDirectPick({ chain, token: sym });
        if (direct !== true) setVenue(direct);
        setPickerOpen(false);
        return;
      }
      // Nothing can sell from Bitcoin or Sui yet (the server refuses them).
      // Said here, at the pick, rather than as a failed quote later.
      if (isBtcLiFiChain(chainId) || isSuiLiFiChain(chainId)) {
        Alert.alert(
          'Not supported yet',
          `Selling ${token.symbol} from ${networkName(chainId)} isn't supported yet. Pick a coin on another network.`,
        );
        return;
      }
      // Deriving a non-EVM address can throw (a locked key store); to her
      // that is the same as not having one.
      const sourceAddress = await sourceAddressFor(chainId, address).catch(() => null);
      if (!sourceAddress) {
        Alert.alert(
          `${networkName(chainId)} wallet not ready`,
          `Set up your ${networkName(chainId)} wallet first, then choose this coin to sell.`,
        );
        return;
      }
      setDirectPick(null);
      setSwapPick({ token: { ...token, chainId }, chainId, sourceAddress });
      setSwapAmount('');
      setPickerOpen(false);
    },
    [address, quote],
  );

  const pickHolding = useMemo(() => {
    if (!swapPick) return undefined;
    const native = isNativeLiFiToken({ chainId: swapPick.chainId, address: swapPick.token.address });
    return holdings.find(
      (h) =>
        h.chainId === swapPick.chainId &&
        (native ? h.native : h.address.toLowerCase() === swapPick.token.address.toLowerCase()),
    );
  }, [swapPick, holdings]);

  // A Solana token moves only with SOL for the fee, and nothing sponsors that
  // yet (§5.4). Unknown holdings never block.
  const needsSol =
    !!swapPick &&
    isSolanaLiFiChain(swapPick.chainId) &&
    !isNativeLiFiToken({ chainId: swapPick.chainId, address: swapPick.token.address }) &&
    !holdingsLoading &&
    !holdings.some((h) => h.chainId === SOLANA_LIFI_CHAIN_ID && h.native && h.balance > 0);

  const overBalance =
    !!pickHolding && Number(swapAmount) > 0 && Number(swapAmount) > pickHolding.balance;

  // MAX: her whole balance, less All Swap's gas reserve when the coin being
  // sold is the one that pays for the swap.
  const onMaxSwap = useCallback(async () => {
    if (!swapPick || !pickHolding) return;
    const { token, chainId } = swapPick;
    let units = ethers.parseUnits(floorBalance(pickHolding.balance, token.decimals), token.decimals);
    const chain = getChainByChainId(chainId);
    if (chain && pickHolding.native) {
      const toBig = (v?: string) => {
        try {
          return v ? BigInt(v) : undefined;
        } catch {
          return undefined;
        }
      };
      const reserve = await nativeGasReserve(chain.key, {
        gasUnits: toBig(swapQuote?.quote.transactionRequest?.gasLimit),
        gasCostWei: toBig(swapQuote?.quote.gasCostWei),
      });
      if (reserve != null) units = spendableNative(units, reserve);
    } else if (pickHolding.native && isSolanaLiFiChain(chainId)) {
      // Selling SOL pays the swap's fee out of the same SOL.
      units = spendableNative(units, SOLANA_SWAP_RESERVE_LAMPORTS);
    }
    setSwapAmount(ethers.formatUnits(units, token.decimals));
  }, [swapPick, pickHolding, swapQuote]);

  const onSellSwapped = useCallback(() => {
    if (!swapPick || !address || !bank) return;
    swapped.start({
      sourceChainId: swapPick.chainId,
      token: swapPick.token,
      amount: swapAmount.trim(),
      walletAddress: address,
      sourceAddress: swapPick.sourceAddress,
      bank: { code: bank.code, account: accountNo, name: accountName || undefined },
      currency: currencyCode,
    });
  }, [swapPick, address, bank, accountNo, accountName, swapAmount, currencyCode, swapped]);

  // A sale stuck before its payout opened (a lost reply, or a swap hash that
  // someone else recorded first) would otherwise block every new sale.
  const confirmCancelSale = useCallback(() => {
    if (!openIntent) return;
    const intent = openIntent;
    Alert.alert(
      'Cancel this sale?',
      'If your swap still arrives, the USDC will be in your wallet on Base and you can sell it here as USDC.',
      [
        { text: 'Keep it', style: 'cancel' },
        {
          text: 'Cancel sale',
          style: 'destructive',
          onPress: async () => {
            setCancellingSale(true);
            try {
              await swapped.cancel(intent);
            } catch (e) {
              Alert.alert('Could not cancel', e instanceof Error && e.message ? e.message : 'Please try again.');
            } finally {
              setCancellingSale(false);
              refreshOpenIntent();
            }
          },
        },
      ],
    );
  }, [openIntent, swapped, refreshOpenIntent]);

  // Resolve the account name so the user sees who they are actually paying
  // before any crypto leaves their wallet.
  useEffect(() => {
    if (!requiresNameCheck || !bank || !isValidAccountNumber(currencyCode, accountNo, destKind)) {
      setAccountName('');
      return;
    }
    let cancelled = false;
    setResolving(true);
    (async () => {
      try {
        const res = await resolveAccountName(bank.code, accountNo);
        if (cancelled) return;
        setAccountName(res.ok ? res.accountName : '');
      } catch {
        if (!cancelled) setAccountName('');
      } finally {
        if (!cancelled) setResolving(false);
      }
    })();
    return () => {
      cancelled = true;
    };
    // destKind belongs here: switching a Nigerian destination between a bank
    // and mobile money changes whether the entered number is valid, and a
    // stale resolved name would then be attached to the wrong destination.
  }, [requiresNameCheck, bank, accountNo, currencyCode, destKind]);

  const onConfirm = useCallback(async () => {
    if (!venue || !address || !bank) return;
    setCreating(true);
    try {
      const created = await createSell({
        amount: amount.trim(),
        chain: venue.chain,
        token: venue.token,
        walletAddress: address,
        bankCode: bank.code,
        accountNumber: accountNo,
        accountName,
        currency: currencyCode,
      });
      setOrder(created);
    } catch (e) {
      Alert.alert(
        'Could not start your sale',
        e instanceof PaycrestError ? e.message : 'Please try again.',
      );
    } finally {
      setCreating(false);
    }
  }, [venue, address, bank, accountNo, accountName, amount, currencyCode]);

  const venueBalance =
    venue && balances ? balanceOf(balances, venue.chain, venue.token) : null;

  const funding: FundingCheck | null = useMemo(
    () =>
      venue
        ? checkFunding(venue, balances, {
            gasSponsored: isGasSponsored(sponsoredRoutes, venue.chain, venue.token),
          })
        : null,
    [venue, balances, sponsoredRoutes],
  );

  /** Hand the order off to the order screen, as "I've sent it" always has.
   *
   *  `sentTx` carries the deposit we just broadcast, so the order screen shows
   *  progress instead of re-asking for a transfer that has already left. It is
   *  route state, so it does not survive the app being killed — the order
   *  screen falls back to the server's paymentClaimedAt for that. */
  const goToOrder = useCallback((id: string, sentTx?: string) => {
    // Tell the server the user asserts they sent the deposit — the only
    // signal that lets an order which later expires with amountPaid=0 read as
    // a genuine unmatched-deposit alarm instead of an ordinary abandoned
    // checkout. Fire-and-forget: the send already happened, so a failed claim
    // must not block navigation — it just means this order degrades to a
    // plain "expired" if it never matches, the same as before.
    claimPayment(id).catch(() => {});
    const to = sentTx
      ? `/wallet/paycrest/order/${id}?sentTx=${encodeURIComponent(sentTx)}`
      : `/wallet/paycrest/order/${id}`;
    router.replace(to as never);
  }, []);

  // A finished swapped sale lands on the order screen like a direct one. One
  // that stopped after its payout opened drops into this screen's own deposit
  // view, whose Send retries the step that failed.
  useEffect(() => {
    if (swapped.stage === 'done' && swapped.order) {
      const to = swapped.depositTx
        ? `/wallet/paycrest/order/${swapped.order.id}?sentTx=${encodeURIComponent(swapped.depositTx)}`
        : `/wallet/paycrest/order/${swapped.order.id}`;
      router.replace(to as never);
      return;
    }
    if (swapped.stage === 'failed') {
      if (swapped.order?.status === 'awaiting_deposit') setOrder(swapped.order);
      refreshOpenIntent();
    }
  }, [swapped.stage, swapped.order, swapped.depositTx, refreshOpenIntent]);

  const onSend = useCallback(async () => {
    if (!order || !address) return;
    setSending(true);
    setSendError(null);
    try {
      const txHash = await sendSponsoredPaycrestDeposit(
        order,
        address,
        nativeBalanceFor(order.chain, balances),
      );
      goToOrder(order.id, txHash);
    } catch (e) {
      // Server and RPC messages here are written to be read — show them.
      setSendError(e instanceof Error ? e.message : 'Could not send. Please try again.');
    } finally {
      setSending(false);
    }
  }, [order, address, balances, goToOrder]);

  // Backing out here is the common case — the user got to the deposit screen
  // and changed their mind. Without this the abandoned order holds the
  // server's one-open-order lock until its window closes, so they cannot start
  // the sale they actually want. See PaycrestOrderService.Cancel.
  const confirmCancel = useCallback(() => {
    if (!order) return;
    Alert.alert(
      'Cancel this sale?',
      'Only cancel if you have NOT sent the crypto yet. If you have already sent it, do not cancel — contact support instead.',
      [
        { text: 'Keep it', style: 'cancel' },
        {
          text: 'Cancel sale',
          style: 'destructive',
          onPress: async () => {
            setCancelling(true);
            try {
              await cancelOrder(order.id);
              // Back to the form with the lock released, rather than to a dead
              // order screen the user would only have to leave again.
              setOrder(null);
              setSendError(null);
              setManualOpen(false);
            } catch (e) {
              Alert.alert(
                'Could not cancel',
                e instanceof PaycrestError ? e.message : 'Please try again.',
              );
            } finally {
              setCancelling(false);
            }
          },
        },
      ],
    );
  }, [order]);

  const routeItems = useMemo(
    () =>
      (quote?.venues ?? []).map((v) => {
        const send = `Send ${v.tokenAmount.toFixed(4)} ${v.token}`;
        const check = checkFunding(v, balances, {
          gasSponsored: isGasSponsored(sponsoredRoutes, v.chain, v.token),
        });
        // Routes stay selectable when they are short — the user may top up,
        // and the reason is spelled out on the form once one is chosen. The
        // point of the annotation is that the user can see which route they
        // CAN take without poking each one blind.
        return { id: venueId(v), label: venueLabel(v), sublabel: `${send}${routeNote(check)}` };
      }),
    [quote, balances, sponsoredRoutes],
  );

  const bankItems = useMemo(
    () => banks.map((b) => ({ id: b.code, label: b.name })),
    [banks],
  );

  // A funding shortfall only blocks the DEFAULT path. Someone selling from
  // MetaMask or an exchange has a perfectly good order to create and an app
  // balance of zero — blocking them outright would be a regression, so the
  // warning carries its own escape hatch and resets whenever the route
  // changes (the next route's verdict is a different question).
  const [fundingOverride, setFundingOverride] = useState(false);
  useEffect(() => setFundingOverride(false), [venue?.chain, venue?.token]);
  const fundingBlocks =
    !fundingOverride &&
    !!funding &&
    !funding.ok &&
    (funding.reason === 'short-token' || funding.reason === 'no-gas');

  const ready =
    !!venue &&
    !!address &&
    !!bank &&
    isValidAccountNumber(currencyCode, accountNo, destKind) &&
    (!requiresNameCheck || !!accountName) &&
    !fundingBlocks;

  const swapReady =
    !!swapPick &&
    !!address &&
    !!bank &&
    isValidAccountNumber(currencyCode, accountNo, destKind) &&
    (!requiresNameCheck || !!accountName) &&
    !!swapQuote &&
    !swapQuoting &&
    !needsSol &&
    !overBalance &&
    !swappedBusy;

  const sellingLabel = swapPick
    ? `${swapPick.token.symbol} on ${networkName(swapPick.chainId)}`
    : directPick
      ? `${directPick.token} on ${CHAIN_LABELS[directPick.chain] ?? directPick.chain}`
      : undefined;

  // ── Deposit instructions ────────────────────────────────────────────────
  if (order) {
    // No key on this device, or a chain we cannot build a tx for: the address
    // is the only thing we can offer, so it opens expanded.
    const canSendInApp =
      !!address && !!CHAINS[order.chain as ChainKey] && !!order.depositAddress && !!order.tokenAmount;
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <Card>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              Send exactly
            </Text>
            <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
              {order.tokenAmount} {order.token}
            </Text>
            <Text variant="caption" style={{ color: colors.textMuted }}>
              on {CHAIN_LABELS[order.chain] ?? order.chain}
              {canSendInApp ? ', from your wallet on this device.' : ', from your connected wallet.'}
            </Text>
          </Card>

          <Card
            style={{
              borderWidth: 1,
              borderColor: expired ? colors.danger : expiringSoon ? colors.warning : colors.border,
            }}
          >
            {expired ? (
              <>
                <Text variant="bodyMed" weight="700" style={{ color: colors.danger }}>
                  This order has expired
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Do not send to this address. Start a new sale instead.
                </Text>
              </>
            ) : (
              <>
                <Text
                  variant="bodySm"
                  style={{ color: expiringSoon ? colors.warning : colors.textSecondary }}
                >
                  Time left to send
                </Text>
                <Text
                  variant="title1"
                  weight="700"
                  style={{ color: expiringSoon ? colors.warning : colors.textPrimary }}
                >
                  {countdown}
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Sending after this runs out leaves your crypto at the provider with no order to
                  match it to.
                </Text>
              </>
            )}
          </Card>

          <SummaryCard>
            <SummaryRow label="You receive" value={`${symbol}${order.amount}`} />
            <SummaryRow label="Into" value={order.payoutAccountName ?? ''} />
            <SummaryRow label="Account" value={order.payoutAccount ?? ''} />
            {order.rate ? <SummaryRow label="Rate" value={`${symbol}${order.rate}`} /> : null}
          </SummaryCard>

          {sendError && (
            <Card style={{ borderWidth: 1, borderColor: colors.danger }}>
              <Text variant="bodySm" style={{ color: colors.danger }}>
                {sendError}
              </Text>
            </Card>
          )}

          {/* Once we are broadcasting, the address is the wrong thing to look
              at: the transfer is already leaving. Show what is happening. */}
          {sending && (
            <DepositProgress
              amountLabel={`${order.tokenAmount} ${order.token}`}
              chainLabel={CHAIN_LABELS[order.chain] ?? order.chain}
            />
          )}

          {/* The wallet holding the key is right here — asking the user to
              copy an address into another app was never the shorter path. */}
          {canSendInApp && (
            <Button
              title={`Send ${order.tokenAmount} ${order.token}`}
              onPress={onSend}
              loading={sending}
              disabled={expired || sending}
              fullWidth
            />
          )}

          {canSendInApp && orderSponsored && (
            <Text variant="caption" style={{ color: colors.textMuted, textAlign: 'center' }}>
              Network fee covered — you don&apos;t need{' '}
              {CHAINS[order.chain as ChainKey]?.nativeCurrency.symbol ?? 'gas'} for this sale.
            </Text>
          )}

          {/* Kept for funds that live somewhere else — an exchange, a hardware
              wallet, MetaMask. Collapsed rather than removed, and out of the
              way entirely while we are sending from this device. */}
          {(manualOpen || !canSendInApp) && !sending ? (
            <Card>
              <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
                To this address
              </Text>
              {/* Full address, never truncated: a send to a mistyped address is
                  irreversible and nothing on our side can recover it. */}
              <CopyableField value={order.depositAddress ?? ''} />
              <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
                Send only {order.token} on {CHAIN_LABELS[order.chain] ?? order.chain}. A different
                token, or the right token on a different network, will not reach the provider.
              </Text>
              <View style={{ marginTop: spacing.md }}>
                <Button
                  title="I've sent it"
                  variant={canSendInApp ? 'secondary' : 'primary'}
                  onPress={() => goToOrder(order.id)}
                  disabled={expired}
                  fullWidth
                />
              </View>
            </Card>
          ) : (
            <Button
              title="Send from another wallet"
              variant="ghost"
              onPress={() => setManualOpen(true)}
              fullWidth
            />
          )}

          {/* Hidden once the user says they have sent — cancelling then would
              claim the order is dead while their deposit is still in flight. */}
          {!order.paymentClaimedAt && (
            <Button
              title="Cancel this sale"
              variant="ghost"
              onPress={confirmCancel}
              loading={cancelling}
              disabled={cancelling}
              fullWidth
            />
          )}
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount, route, payout ───────────────────────────────────────────────
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          {/* Only offer a choice when there is one — see the identical note
              on the buy screen. */}
          {currencyOptions.length > 1 && (
            <SelectRow
              label="Currency"
              value={currencyCode}
              onPress={() => setCurrencyPickerOpen(true)}
            />
          )}

          {openIntent && !swappedBusy && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning, gap: spacing.sm }}>
              <Text variant="bodyMed" weight="700" style={{ color: colors.textPrimary }}>
                You have a sale that didn&apos;t finish
              </Text>
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Nothing is lost. Tap below and we&apos;ll carry on from where it stopped.
              </Text>
              <Button
                title="Finish your sale"
                onPress={() => swapped.resume(openIntent, address ?? undefined)}
                fullWidth
              />
              {canCancelSale(openIntent) && (
                <Button
                  title="Cancel this sale"
                  variant="ghost"
                  onPress={confirmCancelSale}
                  loading={cancellingSale}
                  disabled={cancellingSale}
                  fullWidth
                />
              )}
            </Card>
          )}

          {swappedSellOn && (
            <SelectRow
              label="What are you selling?"
              value={sellingLabel}
              placeholder="USDC or USDT"
              onPress={() => setPickerOpen(true)}
            />
          )}

          {swapPick ? (
            <>
              <AmountInput
                label="You sell"
                value={swapAmount}
                onChangeText={setSwapAmount}
                symbol={swapPick.token.symbol}
                symbolPosition="suffix"
                onMax={pickHolding ? onMaxSwap : undefined}
                helper={
                  pickHolding
                    ? `Balance: ${fmtTokenAmount(pickHolding.balance)} ${swapPick.token.symbol}`
                    : undefined
                }
              />
              {swapQuoting && (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Spinner size="small" />
                  <Text variant="caption" style={{ color: colors.textMuted }}>
                    Checking live rates…
                  </Text>
                </View>
              )}
              {swapQuote && !swapQuoting && (
                <Card>
                  <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                    You receive about
                  </Text>
                  <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
                    {`~${symbol}${Number(swapQuote.fiatEstimate).toLocaleString(undefined, { maximumFractionDigits: 0 })}`}
                  </Text>
                </Card>
              )}
              {swapQuoteError && !swapQuoting && (
                <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
                  <Text variant="bodySm" style={{ color: colors.warning }}>
                    {swapQuoteError}
                  </Text>
                </Card>
              )}
              {overBalance && (
                <Text variant="caption" style={{ color: colors.warning }}>
                  That is more {swapPick.token.symbol} than you have.
                </Text>
              )}
              {needsSol && (
                <Text variant="caption" style={{ color: colors.warning }}>
                  You need a little SOL to send this.
                </Text>
              )}
            </>
          ) : (
          <>
          <AmountInput
            label="You receive"
            value={amount}
            onChangeText={setAmount}
            symbol={symbol}
            symbolPosition="prefix"
            autoFocus
            // Hidden while re-quoting: the previous route's figure belongs to
            // the previous amount.
            helper={
              venue && !quoting
                ? `≈ ${fmtTokenAmount(venue.tokenAmount)} ${venue.token}`
                : undefined
            }
          />

          {quoting && (
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
              <Spinner size="small" />
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Checking live rates…
              </Text>
            </View>
          )}

          {quoteError && !quoting && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
              <Text variant="bodySm" style={{ color: colors.warning }}>
                {quoteError}
              </Text>
            </Card>
          )}

          {quote && quote.venues.length > 0 && (
            <View style={{ gap: spacing.xs }}>
              <SelectRow
                label="You send"
                value={venue ? venueLabel(venue) : undefined}
                placeholder="Choose a network"
                onPress={() => setRouteOpen(true)}
              />
              {/* Unknown balances show nothing rather than a misleading 0. */}
              {venue && venueBalance !== null && (
                <Text
                  variant="caption"
                  style={{
                    color: venueBalance < venue.tokenAmount ? colors.warning : colors.textMuted,
                    textAlign: 'right',
                  }}
                >
                  Balance: {fmtTokenAmount(venueBalance)} {venue.token}
                </Text>
              )}
            </View>
          )}

          </>
          )}

          {/* Where the naira goes — the same for either kind of sale. */}
          <SelectRow
            label="Pay into"
            value={bank?.name}
            placeholder="Choose a destination"
            onPress={() => setBankOpen(true)}
          />
          {/* The field follows the DESTINATION, not the currency: pick MTN in
              Uganda and this asks for a phone number, pick a bank and it asks
              for an account number. */}
          <Input
            label={accountFieldLabel(destKind)}
            value={accountNo}
            onChangeText={(v: string) =>
              setAccountNo(sanitizeAccountNumberInput(currencyCode, v, destKind))
            }
            keyboardType={
              isDigitsOnlyAccountField(currencyCode, destKind) ? 'number-pad' : 'default'
            }
            placeholder={accountFieldPlaceholder(currencyCode, destKind)}
            maxLength={accountNumberMaxLength(currencyCode, destKind)}
          />
          {requiresNameCheck && resolving && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Checking account…
            </Text>
          )}
          {requiresNameCheck && !!accountName && (
            <Text variant="bodySm" weight="600" style={{ color: colors.success }}>
              {accountName}
            </Text>
          )}
          {requiresNameCheck &&
            !resolving &&
            !accountName &&
            isValidAccountNumber(currencyCode, accountNo, destKind) &&
            bank && (
              <Text variant="caption" style={{ color: colors.danger }}>
                We could not verify that account. Check the number and bank.
              </Text>
            )}

          {!swapPick && (
          <>
          {venue && (
            <SummaryCard>
              <SummaryRow
                label="You send"
                value={`${venue.tokenAmount.toFixed(6)} ${venue.token}`}
              />
              <SummaryRow label="Rate" value={`${symbol}${venue.rate} / ${venue.token}`} />
            </SummaryCard>
          )}

          {funding && !funding.ok && (
            <Card style={{ borderWidth: 1, borderColor: colors.warning }}>
              <Text variant="bodySm" style={{ color: colors.warning }}>
                {fundingMessage(funding)}
              </Text>
              {fundingBlocks && (
                <Button
                  title="I'll send from another wallet"
                  variant="ghost"
                  size="sm"
                  onPress={() => setFundingOverride(true)}
                />
              )}
            </Card>
          )}
          </>
          )}

          {swappedBusy && (
            <Card style={{ gap: spacing.sm }}>
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                <Spinner size="small" />
                <Text variant="bodySm" weight="600" style={{ color: colors.textPrimary, flex: 1 }}>
                  {SWAPPED_STAGE_LABELS[swapped.stage]}
                </Text>
              </View>
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Keep the app open. If it closes, come back here and tap Finish your sale.
              </Text>
            </Card>
          )}
          {swapped.stage === 'failed' && swapped.error && (
            <Card style={{ borderWidth: 1, borderColor: colors.danger }}>
              <Text variant="bodySm" style={{ color: colors.danger }}>
                {swapped.error}
              </Text>
            </Card>
          )}

          {swapPick ? (
            <Button
              title="Sell"
              onPress={onSellSwapped}
              loading={swappedBusy}
              disabled={!swapReady}
              fullWidth
            />
          ) : (
            <Button
              title="Continue"
              onPress={onConfirm}
              loading={creating}
              disabled={!ready || quoting || creating || swappedBusy}
              fullWidth
            />
          )}
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — you send the crypto yourself.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <AssetPicker
        visible={swappedSellOn && pickerOpen}
        side="from"
        holdingsOnly
        title="Choose what to sell"
        onSelect={onPick}
        onClose={() => setPickerOpen(false)}
      />
      <PickerSheet
        visible={currencyPickerOpen}
        onClose={() => setCurrencyPickerOpen(false)}
        title="Currency"
        items={currencyOptions}
        selectedId={currencyCode}
        onSelect={(item) => {
          setCurrencyCode(item.id);
          // A new currency means a fresh route and payout destination — the
          // ones held over would either belong to the wrong currency's quote
          // or (for the bank/account) simply not exist in the new list.
          setAmount('');
          setVenue(null);
          setAccountNo('');
          setAccountName('');
          setCurrencyPickerOpen(false);
        }}
        emptyText="No currencies available."
      />
      <PickerSheet
        visible={routeOpen}
        onClose={() => setRouteOpen(false)}
        title="Choose a network"
        items={routeItems}
        selectedId={venue ? venueId(venue) : undefined}
        onSelect={(item) => {
          const picked = quote?.venues.find((v) => venueId(v) === item.id);
          if (picked) setVenue(picked);
          setRouteOpen(false);
        }}
        emptyText="No routes can fill this amount right now."
      />
      <PickerSheet
        visible={bankOpen}
        onClose={() => setBankOpen(false)}
        title="Choose a destination"
        items={bankItems}
        selectedId={bank?.code}
        searchable
        onSelect={(item) => {
          const next = banks.find((b) => b.code === item.id) ?? null;
          // Switching between kinds invalidates what was typed — a phone
          // number is not an account number, and each passes the other's rule
          // by accident. See accountValueSurvivesKindChange.
          if (!accountValueSurvivesKindChange(destKind, next?.type)) {
            setAccountNo('');
            setAccountName('');
          }
          setBank(next);
          setBankOpen(false);
        }}
        emptyText="No destinations available."
      />
    </SafeAreaView>
  );
}
