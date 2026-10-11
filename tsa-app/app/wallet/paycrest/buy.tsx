// app/wallet/paycrest/buy.tsx
//
// Buy stablecoins with local fiat (the selected ramp currency) through
// Paycrest's provider network.
//
// Non-custodial end to end: the user transfers fiat straight to a liquidity
// provider's bank account, and the stablecoin lands in their own connected
// wallet. TSA brokers and records; it never holds either side.
//
// Two things drive the design, both learned from live testing:
//
//   1. Liquidity is not fixed. Providers join and leave the book and price by
//      order size — the same pair moved 4x overnight, and one day a whole
//      token had no onramp provider at all. So the route list is built from a
//      quote for the amount actually typed, never a hardcoded list. A route
//      the user can see is one the order will succeed on.
//   2. An order that expires while the user is still transferring can strand
//      their money at the provider's bank with no automatic refund. Hence the
//      prominent countdown and the warning well before it runs out.
//
// Flow: amount -> live routes -> confirm -> provider account + countdown ->
// the user pays in their bank app -> the order screen takes over.
//
// Phase 2 (converted buys): the user now names the token AND chain they want
// up front (AssetPicker), rather than the screen inferring a chain from
// whatever Paycrest could fill. The quote for that exact pair comes back as
// either a direct venue, a labelled CONVERSION (Paycrest fills something
// else into our treasury, then a cross-chain route delivers what was asked
// for), or nothing at all — in which case the closest fillable route is
// offered as an explicit choice, never swapped in silently. See
// services/paycrestApi.ts's isConvertedVenue/describeConvertedVenue.
import React, { useCallback, useEffect, useRef, useState } from 'react';
import { View, ScrollView, KeyboardAvoidingView, Platform, Alert } from 'react-native';
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
import AsyncStorage from '@react-native-async-storage/async-storage';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import { useCountdown } from '@/hooks/useCountdown';
import { rampCurrencyLabel, useRampCurrency } from '@/hooks/useRampCurrency';
import { useScreenTitle } from '@/hooks/useScreenTitle';
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
  createBuy,
  getPayoutInstitutions,
  getQuote,
  isConvertedVenue,
  selectVenue,
  describeConvertedVenue,
  formatPromisedAmount,
  formatEstimate,
  formatEta,
  PaycrestError,
  type PayoutInstitution,
  type PaycrestOrder,
  type PaycrestQuote,
  type PaycrestVenue,
  type RequestedAsset,
} from '@/services/paycrestApi';
import {
  cancelRampOrder,
  claimRampPayment,
  createRampBuy,
  getRampQuote,
  itIntentIdOf,
  mergeBuyQuotes,
  openOrderOf,
  priceMovedQuote,
  quotedRateOf,
  type RampVenue,
} from '@/services/rampApi';
import { itIntentRoute } from '@/utils/itBuyOrder';
import { poolBuyParams } from '@/utils/poolCard';
import { IT_AGREEMENT_REQUIRED } from '@/services/itApi';
import { FeeLines, fiatText } from '@/components/ramp/FeeLines';
import { formatTokenAmount } from '@/services/p2p';
import { promptKycRefusal } from '@/utils/kycRefusal';
// The destination token AND chain picker — reused verbatim from the swap
// flow rather than built again here. Its callback is already
// `(token: LiFiToken, chainId: number)`, exactly the shape a "what do you
// want to receive" picker needs.
import AssetPicker from '@/components/assets/AssetPicker';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import { getLiFiTokens, findTokenBySymbolForChain, isSolanaLiFiChain, type LiFiToken } from '@/services/lifi';
// Her Solana address is derived from the same seed as the EVM wallet — read
// through the wallet service that already owns that derivation, never
// re-derived here.
import { getSolanaAddress } from '@/services/chains/solanaWallet';
import { getChainByChainId, getChainDisplay, getChain, type ChainKey } from '@/constants/chains';

/** Debounce on the amount field: every keystroke would otherwise fan a live
 *  quote out across every network we support. */
const QUOTE_DEBOUNCE_MS = 450;

/** Warn while there is still time to act, not once it is hopeless. A transfer
 *  confirmed in ~10s in testing, but the user still has to open their bank app
 *  and type. */
const EXPIRY_WARNING_SECONDS = 300;

const CHAIN_LABELS: Record<string, string> = {
  base: 'Base',
  polygon: 'Polygon',
  bsc: 'BNB Smart Chain',
  arbitrum: 'Arbitrum',
  ethereum: 'Ethereum',
};

/** A REQUESTED chain can be anywhere LiFi routes to — Optimism, Avalanche,
 *  Sonic — not just the handful Paycrest itself settles on, so the fixed
 *  table above is only a first guess. Falls back to the chain registry's own
 *  display name, then the raw key, rather than ever showing nothing. */
const chainLabel = (key?: string): string => {
  if (!key) return '—';
  return CHAIN_LABELS[key] ?? getChainDisplay(key as ChainKey)?.name ?? key;
};

/** Where the picker starts before the user has chosen anything — Base is
 *  Paycrest's deepest-liquidity settlement chain today (see
 *  paycrest_quote.go's token/chain preference comments), so it is also the
 *  route most likely to be fillable directly on first render. */
const DEFAULT_CHAIN_KEY: ChainKey = 'base';
const DEFAULT_CHAIN_ID = getChain(DEFAULT_CHAIN_KEY)?.chainId ?? 8453;

/** The refund account is insurance against a rare failure, not part of the
 *  purchase — asking for it on every buy is friction for something most users
 *  will never use. Remembered after the first time and shown as one line.
 *  Scoped per currency: a NGN account number is meaningless as a GHS refund
 *  destination. */
const REFUND_STORE_KEY = (currency: string) => `paycrest.refundAccount.v1.${currency}`;

/** Chains a Naira buy can be quoted on: the wallet's EVM registry, plus
 *  Solana — LiFi delivers there and the destination transaction's own balance
 *  changes prove the landing, so it is as settleable as any EVM chain. */
const isBuyableChain = (chainId: number) =>
  getChainByChainId(chainId) !== undefined || isSolanaLiFiChain(chainId);

/** The chain key for a picked chain id. Solana is not in the EVM registry, so
 *  it is named here rather than looked up — every other chain still comes from
 *  the registry, never from a table that could drift out of date. */
const chainKeyForPicked = (chainId: number): string | undefined =>
  getChainByChainId(chainId)?.key ?? (isSolanaLiFiChain(chainId) ? 'solana' : undefined);

export default function PaycrestBuyScreen() {
  const { colors, spacing } = useTheme();
  const { address, ready: walletReady } = useSignableWallet();
  // The asset the user picked upstream — only a starting point for the
  // token+chain picker below (see `want`), never a filter: the user can
  // change either one, and the quote says plainly when the pair they land on
  // cannot be filled.
  const { asset, currency: currencyParam, openPicker, pool, chainId, chain, tokenAddress } = useLocalSearchParams<{
    asset?: string;
    currency?: string;
    /** Set by the IT hub's "Any other token" row — she arrives with nothing
     *  pre-selected, so the picker opens immediately instead of waiting for
     *  her to notice and tap the token field herself. */
    openPicker?: string;
    /** Set by the pinned TSA Connect card (utils/poolCard): pool=1 asks the
     *  pool-only quote, and the card's token on its chain is preselected. */
    pool?: string;
    chainId?: string;
    chain?: string;
    tokenAddress?: string;
  }>();
  const poolBuy = poolBuyParams({ pool, chainId, chain, tokenAddress, asset, currency: currencyParam });
  // A pool buy settles in naira only, so it overrides both the route's
  // currency and the saved ramp choice.
  const routeCurrency = poolBuy.currency ?? currencyParam;

  // The currency this order settles in — NOT the display currency (see
  // hooks/useRampCurrency.ts). Restricted to 'paycrest' so the picker below
  // can never offer a currency this screen would fail to quote — provider AND
  // direction, since Paycrest sells UGX and TZS but does not buy them, so a
  // provider-only filter would default a Ugandan user into a currency this
  // screen can never price. A currency arriving via the route
  // param (the hub already resolved one) is applied immediately so the first
  // render doesn't briefly show a different default while AsyncStorage loads.
  const {
    code: currencyCode,
    currency: currencyConfig,
    setCode: setCurrencyCode,
    options: currencyOptions,
  } = useRampCurrency('paycrest', 'buy');
  const symbol = currencyConfig.symbol;
  const currencyLabel = rampCurrencyLabel(currencyCode);
  useScreenTitle(`Buy with ${currencyLabel}`);
  useEffect(() => {
    if (routeCurrency) setCurrencyCode(routeCurrency);
    // Applying the hub's choice is a one-time sync on arrival, not a binding —
    // re-running on every setCurrencyCode identity change would fight the
    // user's own in-screen picker selection.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [routeCurrency]);
  const [currencyPickerOpen, setCurrencyPickerOpen] = useState(false);

  const [amount, setAmount] = useState('');
  const [quote, setQuote] = useState<PaycrestQuote | null>(null);
  const [venue, setVenue] = useState<PaycrestVenue | null>(null);
  const [quoting, setQuoting] = useState(false);
  const [quoteError, setQuoteError] = useState<string | null>(null);
  // A pool-only quote the pool could not fill: the venues shown are other offers.
  const [poolFallback, setPoolFallback] = useState(false);

  // ── What the user wants to receive: token AND chain, both explicit ──────
  //
  // This is the whole point of Task 13 (and, per Task 9, keyed by chain id +
  // address rather than chain key + symbol): the screen used to infer a
  // chain from whatever the book could fill and only let the user pick among
  // THOSE routes. Now the user names the destination first, and the quote
  // either confirms it is fillable (direct or converted) or says plainly
  // that it is not — never the other way around.
  //
  // `address` starts empty for the route-param default (Base has no LiFiToken
  // to read an address off yet) and for the "use X instead" alternative —
  // the effect below resolves it from the chain's own token list by symbol.
  // AssetPicker.onSelect always supplies it directly, so it never needs
  // resolving there.
  const [want, setWant] = useState<RequestedAsset>(
    poolBuy.want ?? {
      token: (asset || 'USDC').toString().toUpperCase(),
      chainId: DEFAULT_CHAIN_ID,
      chain: DEFAULT_CHAIN_KEY,
      address: '',
    },
  );
  // Kept only for the picker's icon/name — every lookup that matters (the
  // quote, the order) goes through `want`, which survives even when this
  // hasn't resolved (e.g. the "use X instead" choice below, which has no
  // LiFiToken object at all).
  const [wantTokenMeta, setWantTokenMeta] = useState<LiFiToken | null>(null);
  const [tokenPickerOpen, setTokenPickerOpen] = useState(false);
  useEffect(() => {
    // Only ever opens it — never closes it back if the param disappears from
    // a later re-render, which would fight her own use of the picker.
    if (openPicker) setTokenPickerOpen(true);
  }, [openPicker]);
  // Tagged with the chain id it was actually fetched FOR — not just "the
  // current list" — because getLiFiTokens resolves asynchronously: on the
  // very render `want.chainId` changes, this can still hold the PREVIOUS
  // chain's tokens, and a symbol like USDT/USDC/DAI recurs on almost every
  // EVM chain. Every consumer below goes through findTokenBySymbolForChain,
  // which refuses a match when this tag disagrees with what's wanted, rather
  // than risk pinning a wrong-chain address to the new chain id.
  const [pickerTokens, setPickerTokens] = useState<{ chainId: number; tokens: LiFiToken[] }>({
    chainId: DEFAULT_CHAIN_ID,
    tokens: [],
  });

  // Refund destination. Paycrest requires one on every onramp, because it
  // cannot know who will pay until the transfer arrives — so this is the only
  // thing that decides where money goes back to if the order fails. Asked
  // once and kept, rather than per purchase.
  //
  // The list itself comes from getPayoutInstitutions, already in the
  // vocabulary the order expects: NIBSS codes for NGN, Paycrest's own codes
  // elsewhere. Re-fetched (and the remembered selection re-validated) whenever
  // the currency changes, since a NGN refund bank is meaningless once the
  // order settles in GHS.
  const [banks, setBanks] = useState<PayoutInstitution[]>([]);
  const [refundBank, setRefundBank] = useState<PayoutInstitution | null>(null);
  const [refundOpen, setRefundOpen] = useState(false);
  // The refund destination can be mobile money too — in Uganda it always is —
  // and the field must ask for a phone number when it is. Missing type reads
  // as a bank, which is what every currency's majority destination is.
  const refundKind = refundBank?.type ?? 'bank';
  const [refundAccount, setRefundAccount] = useState('');

  const [order, setOrder] = useState<PaycrestOrder | null>(null);
  const [creating, setCreating] = useState(false);

  // Guards a late quote response from overwriting a newer one — the user types
  // faster than the network answers, and a stale route list would let them
  // pick something that cannot fill their current amount.
  const quoteSeq = useRef(0);

  const [editingRefund, setEditingRefund] = useState(false);

  useEffect(() => {
    let cancelled = false;
    setRefundBank(null);
    getPayoutInstitutions(currencyCode)
      .then(async (list) => {
        if (cancelled) return;
        setBanks(list);
        try {
          const saved = await AsyncStorage.getItem(REFUND_STORE_KEY(currencyCode));
          if (!saved) {
            setEditingRefund(true); // first time: ask
            return;
          }
          const { code, account } = JSON.parse(saved) as { code: string; account: string };
          const found = list.find((b) => b.code === code);
          if (found && account) {
            setRefundBank(found);
            setRefundAccount(account);
            setEditingRefund(false);
          } else {
            setEditingRefund(true);
          }
        } catch {
          setEditingRefund(true);
        }
      })
      .catch(() => {
        if (!cancelled) setEditingRefund(true);
      });
    return () => {
      cancelled = true;
    };
  }, [currencyCode]);

  // Token list for whichever chain is currently wanted — used both to resolve
  // the wanted token's icon below and, when `want.address` isn't already
  // known (the route-param default, or right after "use X instead"), its
  // on-chain address. AssetPicker.onSelect supplies the address directly, so
  // this never re-resolves it there.
  useEffect(() => {
    let active = true;
    const chainId = want.chainId;
    getLiFiTokens(chainId)
      .then((tokens) => {
        // Tag the list with the chain id it was fetched FOR — see
        // pickerTokens's own doc comment for why this matters more than it
        // looks like it should.
        if (active) setPickerTokens({ chainId, tokens });
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [want.chainId]);

  // Resolve the wanted token's own metadata (icon) once its chain's list
  // loads — best-effort only; `want` already carries everything the quote
  // and the order actually need.
  useEffect(() => {
    const match = findTokenBySymbolForChain(
      pickerTokens.tokens,
      pickerTokens.chainId,
      want.chainId,
      want.token,
    );
    if (match) setWantTokenMeta(match);
  }, [pickerTokens, want.chainId, want.token]);

  // Fill in the address once it's known — the quote effect below waits for
  // this rather than guessing at getLiFiToken(chainId, addressOrSymbol),
  // which takes an address, not a symbol.
  useEffect(() => {
    if (want.address) return;
    const wantedChainId = want.chainId;
    const match = findTokenBySymbolForChain(
      pickerTokens.tokens,
      pickerTokens.chainId,
      wantedChainId,
      want.token,
    );
    if (match) {
      setWant((w) => (w.address || w.chainId !== wantedChainId ? w : { ...w, address: match.address }));
    }
  }, [pickerTokens, want.address, want.chainId, want.token]);

  // Re-quote on amount, currency, or a change of what the user wants to
  // receive. The requested (chain, token) is sent on every call — see
  // getQuote's doc comment — so the server can price a converted route for
  // THIS pair once it is wired to (today it just ignores the extra params
  // and answers with direct venues only, which the matching below already
  // handles correctly).
  useEffect(() => {
    const raw = amount.trim();
    // want.address is empty until the address-resolution effect above lands
    // (route-param default, or right after "use X instead") — wait for it
    // rather than quoting a pair the server can't identify yet.
    if (!raw || Number(raw) <= 0 || !want.chain || !want.address) {
      setQuote(null);
      setVenue(null);
      setQuoteError(null);
      setPoolFallback(false);
      return;
    }
    const seq = ++quoteSeq.current;
    setQuoting(true);
    const t = setTimeout(async () => {
      try {
        // The router's direct venues (every provider, best per pair, in the
        // server's order) plus the converted ones only /paycrest prices.
        // Either can fail alone — Paycrest's naira book has been empty.
        // From the pool card, the pool-only quote stands alone: it already
        // carries the other offers when the pool cannot fill.
        const [direct, converted] = await Promise.allSettled(
          poolBuy.pool
            ? [getRampQuote('buy', raw, currencyCode, want, { pool: true }), Promise.resolve(null)]
            : [getRampQuote('buy', raw, currencyCode, want), getQuote('buy', raw, currencyCode, want)],
        );
        if (seq !== quoteSeq.current) return;
        const merged = mergeBuyQuotes(
          direct.status === 'fulfilled' ? direct.value : null,
          converted.status === 'fulfilled' ? converted.value : null,
        );
        if (!merged) {
          // Both failed: the router's reason first — it speaks for every provider.
          const failed = [direct, converted].find((r): r is PromiseRejectedResult => r.status === 'rejected');
          throw failed ? failed.reason : new Error('no quote');
        }
        const q = merged as PaycrestQuote;
        setQuote(q);
        setPoolFallback(poolBuy.pool && direct.status === 'fulfilled' && direct.value.poolAvailable === false);
        setQuoteError(null);
        // Never infer a substitute here. Either the exact pair the user asked
        // for is fillable — directly or via a labelled conversion — or venue
        // stays null and the screen says so, offering the alternative as a
        // choice rather than silently swapping it in (see the render below).
        // selectVenue is the one thing that guarantees this, and it is
        // unit-tested on its own in services/__tests__/paycrestApi.conversion.test.ts
        // — nothing here re-implements or shadows that rule.
        setVenue(selectVenue(q.venues, want));
      } catch (e) {
        if (seq !== quoteSeq.current) return;
        setQuote(null);
        setVenue(null);
        setPoolFallback(false);
        // The server's wording is written for users — "no provider can fill
        // that amount right now" is actionable in a way "error" is not.
        setQuoteError(
          e instanceof PaycrestError
            ? e.message
            : 'Could not fetch a rate. Check your connection.',
        );
      } finally {
        if (seq === quoteSeq.current) setQuoting(false);
      }
    }, QUOTE_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [amount, currencyCode, want, poolBuy.pool]);

  const { label: countdown, expired, secs } = useCountdown(order?.validUntil);
  const expiringSoon = !expired && secs > 0 && secs <= EXPIRY_WARNING_SECONDS;

  const onConfirm = useCallback(async () => {
    if (!venue || !address || !refundBank || !want.chain || !want.address) return;
    setCreating(true);
    try {
      // A direct venue, or a converted one the router priced (it carries a
      // handle), goes through the ramp router, held to the rate she saw.
      // Paycrest's own converted venue (no handle) keeps its route — except
      // on a pool buy, which always goes to the router flagged pool.
      const routerVenue = !!(venue as RampVenue).handle;
      const created = isConvertedVenue(venue) && !routerVenue && !poolBuy.pool
        ? await createBuy({
          amount: amount.trim(),
          // The settlement leg — what Paycrest itself is asked to fill.
          chain: venue.chain,
          token: venue.token,
          // What the user actually asked for, by chain id + address — the
          // server derives decimals itself, so that never gets sent. Sent
          // explicitly and always, whether or not this venue converts: on a
          // direct match the two pairs are identical anyway, and the server
          // defaults one from the other regardless — this is just never
          // leaving it to inference.
          requestedChainId: want.chainId,
          requestedTokenAddress: want.address,
          requestedToken: want.token,
          // Where a destination the EVM wallet cannot receive on is delivered
          // (Solana). Absent everywhere else, where the wallet IS the recipient.
          ...(want.destinationAddress ? { destinationAddress: want.destinationAddress } : {}),
          // Holds the order to the fee she was just shown.
          ...(isConvertedVenue(venue) && venue.markupBps ? { quotedMarkupBps: venue.markupBps } : {}),
          walletAddress: address,
          refundBankCode: refundBank.code,
          refundAccount,
          currency: currencyCode,
        })
        : await createRampBuy({
            amount: amount.trim(),
            chain: venue.chain,
            token: venue.token,
            walletAddress: address,
            refundBankCode: refundBank.code,
            refundAccount,
            currency: currencyCode,
            // Held to the all-in rate she was shown (spec §14); try the
            // provider she was shown first — the router falls back.
            quotedRate: quotedRateOf(venue as RampVenue),
            ...((venue as RampVenue).handle ? { handle: (venue as RampVenue).handle } : {}),
            // From the pinned card: the pool's order or none (I-3).
            ...(poolBuy.pool ? { pool: true as const } : {}),
            ...(isConvertedVenue(venue)
              ? {
                  requestedChainId: want.chainId,
                  requestedTokenAddress: want.address,
                  requestedToken: want.token,
                  ...(want.chain ? { requestedChain: want.chain } : {}),
                  ...(want.destinationAddress ? { destinationAddress: want.destinationAddress } : {}),
                }
              : {}),
          });
      // Remember it only once an order actually succeeded with it.
      AsyncStorage.setItem(
        REFUND_STORE_KEY(currencyCode),
        JSON.stringify({ code: refundBank.code, account: refundAccount }),
      ).catch(() => {});
      const itIntent = itIntentIdOf(created);
      if (itIntent) {
        // The company wallet or Flutterwave fills it: an Instant Transfer
        // purchase, whose screen has the pay-into card, the rate hold,
        // "I've sent the transfer" and Cancel.
        router.replace(itIntentRoute(itIntent) as never);
        return;
      }
      setOrder(created);
    } catch (e) {
      // The rate moved past what she confirmed: show the new one and let her
      // confirm again — never open at a price she did not see.
      const fresh = priceMovedQuote(e);
      if (fresh) {
        setQuote(fresh as PaycrestQuote);
        setVenue(selectVenue(fresh.venues, want));
        Alert.alert('The rate changed', 'Check the new rate and confirm again.');
        return;
      }
      // One purchase waits for payment at a time: offer the way to it, or
      // out of it, instead of a dead end (spec §6).
      const open = openOrderOf(e);
      if (open) {
        Alert.alert(
          'You have a purchase waiting for payment',
          'Pay into it, or cancel it to start a new one. Only cancel if you have not sent the money.',
          [
            { text: 'Keep it', style: 'cancel' },
            {
              text: 'Cancel it',
              style: 'destructive',
              onPress: () => {
                cancelRampOrder(open.orderId)
                  .then(() => Alert.alert('Purchase cancelled', 'You can start a new purchase now.'))
                  .catch((err) =>
                    Alert.alert('Could not cancel', err instanceof PaycrestError ? err.message : 'Please try again.'));
              },
            },
            { text: 'View', onPress: () => router.push(itIntentRoute(open.orderId) as never) },
          ],
        );
        return;
      }
      // A routed Flutterwave buy needs the IT agreement; the hub owns its gate.
      if (e instanceof PaycrestError && e.code === IT_AGREEMENT_REQUIRED) {
        router.replace('/wallet/it' as never);
        return;
      }
      // Not verified (or verified with no legal name): offer the next step
      // instead of a dead-end "could not start" alert.
      if (e instanceof PaycrestError && promptKycRefusal(e.code, e.message)) return;
      Alert.alert(
        'Could not start your purchase',
        e instanceof PaycrestError ? e.message : 'Please try again.',
      );
    } finally {
      setCreating(false);
    }
  }, [venue, address, amount, refundBank, refundAccount, currencyCode, want, poolBuy.pool]);

  // The explicit alternative offered when nothing can fill the exact pair the
  // user asked for, even with a conversion — never auto-selected, only
  // switched to on a deliberate tap (see the "Use X instead" button below).
  // No LiFiToken is available for it, so address starts empty — the
  // token-list effect above resolves it from the new chain's own list, the
  // same as the route-param default.
  const pickAlternativeVenue = useCallback((v: PaycrestVenue) => {
    // v.chain is a raw string off the wire, not a type-checked ChainKey —
    // matches DEFAULT_CHAIN_ID's own guard above rather than assuming every
    // value the backend ever sends is one of the keys this build knows about.
    const chainId = getChain(v.chain as ChainKey)?.chainId;
    if (chainId == null) return;
    setWant({ token: v.token.toUpperCase(), chainId, chain: v.chain, address: '' });
    setWantTokenMeta(null);
  }, []);

  // ── Payment instructions ────────────────────────────────────────────────
  if (order) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          <Card>
            <Text variant="bodySm" style={{ color: colors.textSecondary }}>
              Transfer exactly
            </Text>
            <Text variant="display" weight="700" style={{ color: colors.textPrimary }}>
              {symbol}{order.payAmount}
            </Text>
            <Text variant="caption" style={{ color: colors.textMuted }}>
              The service fee is already priced into your rate, so this is exactly the amount
              you entered.
            </Text>
          </Card>

          <Card
            style={{
              borderWidth: 1,
              borderColor: expired
                ? colors.danger
                : expiringSoon
                  ? colors.warning
                  : colors.border,
            }}
          >
            {expired ? (
              <>
                <Text variant="bodyMed" weight="700" style={{ color: colors.danger }}>
                  This order has expired
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Do not send money to this account. If you already sent it, open the order and
                  tap Get help — we will trace it for you.
                </Text>
              </>
            ) : (
              <>
                <Text
                  variant="bodySm"
                  style={{ color: expiringSoon ? colors.warning : colors.textSecondary }}
                >
                  Time left to pay
                </Text>
                <Text
                  variant="title1"
                  weight="700"
                  style={{ color: expiringSoon ? colors.warning : colors.textPrimary }}
                >
                  {countdown}
                </Text>
                <Text variant="caption" style={{ color: colors.textMuted }}>
                  Send the transfer now. Paying after this runs out can leave your money with the
                  provider, and it will not arrive automatically.
                </Text>
              </>
            )}
          </Card>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Pay into
            </Text>
            {/* Copyable, because a mistyped account number on a live transfer
                is not something we can undo. */}
            <CopyableField label="Bank" value={order.payInstitution ?? ''} />
            <CopyableField label="Account number" value={order.payAccountNumber ?? ''} />
            <CopyableField label="Account name" value={order.payAccountName ?? ''} />
            <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.sm }}>
              This account belongs to the liquidity provider filling your order, not to TSA. It is
              valid for this order only — never reuse it.
            </Text>
          </Card>

          <SummaryCard>
            {order.routeKind === 'converted' ? (
              <>
                {/* order.tokenAmount/order.token are the SETTLEMENT leg —
                    what Paycrest itself fills, to our treasury — not what
                    lands in the user's wallet. Showing that here would be
                    exactly the silent substitution this phase exists to
                    remove, so the requested-token figure is what "You
                    receive" must show on a converted order. It is an
                    ESTIMATE now, not a guarantee — priced against a
                    round-trip budget, not a promised minimum. */}
                <SummaryRow
                  label="You receive"
                  value={formatEstimate(order) ?? `${order.tokenAmount} ${order.requestedToken ?? order.token}`}
                  hint="Estimate — the final amount can differ slightly"
                />
                {order.promisedAmount && order.requestedTokenDecimals != null ? (
                  <SummaryRow
                    label="Guaranteed minimum"
                    value={`${formatTokenAmount(order.promisedAmount, order.requestedTokenDecimals)} ${order.requestedToken ?? order.token}`}
                  />
                ) : null}
                <SummaryRow label="Network" value={chainLabel(order.requestedChain)} />
                <SummaryRow
                  label="Route"
                  value={`via ${order.token} on ${chainLabel(order.chain)}`}
                />
              </>
            ) : (
              <>
                <SummaryRow label="You receive" value={`${order.tokenAmount} ${order.token}`} />
                <SummaryRow label="Network" value={chainLabel(order.chain)} />
              </>
            )}
            <SummaryRow label="Rate" value={`${symbol}${order.rate} / ${order.token}`} />
          </SummaryCard>

          <Card>
            <Text variant="bodySm" weight="600" style={{ color: colors.textSecondary }}>
              Delivered to your wallet
            </Text>
            {/* Full address, never truncated — the user is entitled to verify
                where their crypto is going. */}
            <CopyableField value={order.walletAddress} />
          </Card>

          <Button
            title="I've sent the money"
            onPress={() => {
              // Tell the server the user asserts they paid — the only signal
              // that lets an order which later expires with amountPaid=0 read
              // as a genuine unmatched-deposit alarm instead of an ordinary
              // abandoned checkout. Fire-and-forget: the user has already
              // sent the money, so a failed claim must not block navigation —
              // it just means this order degrades to a plain "expired" if it
              // never matches, the same as before this existed.
              claimRampPayment(order.id).catch(() => {});
              router.replace(`/wallet/paycrest/order/${order.id}` as never);
            }}
            disabled={expired}
            fullWidth
          />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // ── Amount + route ──────────────────────────────────────────────────────
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView contentContainerStyle={{ padding: spacing.md, gap: spacing.md }}>
          {/* Only offer a choice when there is one — today that's true for
              every Paycrest-ramped currency (just NGN), but the picker grows
              on its own the day a second one is added to the registry. */}
          {currencyOptions.length > 1 && (
            <SelectRow
              label="Currency"
              value={currencyCode}
              onPress={() => setCurrencyPickerOpen(true)}
            />
          )}

          {/* Token AND chain, both explicit — the inversion this task exists
              for. The old screen inferred a chain from whatever the book
              could fill; this asks first, then tells the truth about
              whether that pair is fillable. */}
          <SelectRow
            label="You want"
            value={want.chain ? `${want.token} on ${chainLabel(want.chain)}` : `${want.token} — unsupported network`}
            onPress={() => setTokenPickerOpen(true)}
            leftElement={
              wantTokenMeta ? (
                <TokenAvatar symbol={wantTokenMeta.symbol} iconUrl={wantTokenMeta.logoURI} size={24} />
              ) : undefined
            }
          />

          <AmountInput
            label="You pay"
            value={amount}
            onChangeText={setAmount}
            symbol={symbol}
            symbolPosition="prefix"
            autoFocus
          />

          {/* The payoff sits directly under the input, because "what do I get"
              is the question the amount field is asking. Everything else on
              this screen is secondary to it. */}
          {!want.chain && amount.trim() ? (
            <Card>
              <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                {`This network isn't supported for a ${currencyLabel} purchase yet — choose a different one.`}
              </Text>
            </Card>
          ) : null}
          {(quoting || venue || quoteError || (quote && !venue)) && (
            <Card>
              {quoting ? (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm }}>
                  <Spinner size="small" />
                  <Text variant="bodySm" style={{ color: colors.textMuted }}>
                    Finding the best rate…
                  </Text>
                </View>
              ) : quoteError ? (
                <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                  {quoteError}
                </Text>
              ) : venue ? (
                <>
                  <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                    You receive
                  </Text>
                  <Text variant="title1" weight="700" style={{ color: colors.textPrimary }}>
                    {isConvertedVenue(venue)
                      ? (formatEstimate(venue) ?? `${formatPromisedAmount(venue) ?? venue.tokenAmount} ${venue.requestedToken}`)
                      : `${Number(venue.tokenAmount).toFixed(6)} ${venue.token}`}
                  </Text>
                  <Text variant="caption" style={{ color: colors.textMuted }}>
                    {/* The rate is always priced against the SETTLEMENT leg
                        (Paycrest quotes ₦ per venue.token, not per the
                        requested token), so chain and token must be paired
                        from the same leg here — pairing venue.chain's rate
                        with the requested chain read as "₦1650 per USDC" on
                        a line that says "on Optimism" when USDC never
                        touches Optimism. The Route line below already states
                        the requested side; this one is explicitly labelled
                        as the settlement leg. */}
                    {isConvertedVenue(venue)
                      ? `Settles as ${venue.token} on ${chainLabel(venue.chain)}`
                      : `on ${chainLabel(venue.chain)}`}{' '}
                    · {fiatText(venue.rate, symbol)} per {venue.token}
                  </Text>
                  {/* Every provider fee as its own line (spec §14); "You
                      receive" above is already after all of them. */}
                  {/* Empty for Paycrest's converted venue, whose markup is
                      inside its rate. */}
                  <FeeLines fees={(venue as RampVenue).fees ?? []} token={venue.token} symbol={symbol} />
                  {/* A converted route is labelled honestly, every time — this
                      is the one thing that must never be quiet: what the
                      route settles as, that it converts, and roughly how
                      long that takes. Route/Est. time/Fee mirror the same
                      DetailRow presentation the all-swap screen already uses
                      for a route breakdown. */}
                  {isConvertedVenue(venue) && (
                    <>
                      <Text
                        variant="bodySm"
                        style={{ color: colors.warning, marginTop: spacing.sm }}
                      >
                        {describeConvertedVenue(venue, chainLabel)}
                      </Text>
                      <SummaryRow
                        label="Route"
                        value={`${venue.viaToken} on ${chainLabel(venue.viaChain)} → ${venue.requestedToken} on ${chainLabel(venue.requestedChain)}`}
                      />
                      <SummaryRow label="Est. time" value={formatEta(venue.etaSeconds)} />
                      {/* No separate "Conversion fee" row — the markup is
                          already inside the one rate shown above. */}
                    </>
                  )}
                </>
              ) : quote && !venue ? (
                // Neither a direct nor a converted route exists for the exact
                // pair the user asked for. Say so plainly, and offer the
                // closest fillable route as a CHOICE the user must tap —
                // never something this screen picks for them.
                <>
                  <Text variant="bodySm" style={{ color: colors.textSecondary }}>
                    No route can deliver {want.token} on {chainLabel(want.chain)} right now, even
                    with a conversion.
                  </Text>
                  {quote.best ? (
                    <Button
                      title={`Use ${quote.best.token} on ${chainLabel(quote.best.chain)} instead`}
                      variant="ghost"
                      size="sm"
                      onPress={() => quote.best && pickAlternativeVenue(quote.best)}
                    />
                  ) : (
                    <Text variant="caption" style={{ color: colors.textMuted, marginTop: spacing.xs }}>
                      Try a different amount, or check back shortly.
                    </Text>
                  )}
                </>
              ) : null}
            </Card>
          )}
          {poolFallback && (
            <Text variant="caption" color="textSecondary" style={{ marginTop: spacing.sm }}>
              {"TSA Connect can't fill this amount right now — these are other offers."}
            </Text>
          )}

          {/* Demoted: insurance, not part of the purchase. One line once known. */}
          {!editingRefund && refundBank ? (
            <SelectRow
              label="Refund account"
              value={`${refundBank.name} · ${refundAccount}`}
              onPress={() => setEditingRefund(true)}
            />
          ) : (
            <>
              <SelectRow
                label="Refund account"
                value={refundBank?.name}
                placeholder="Where should we refund you?"
                onPress={() => setRefundOpen(true)}
              />
              <Input
                label={accountFieldLabel(refundKind)}
                value={refundAccount}
                onChangeText={(v: string) =>
                  setRefundAccount(sanitizeAccountNumberInput(currencyCode, v, refundKind))
                }
                keyboardType={
                  isDigitsOnlyAccountField(currencyCode, refundKind) ? 'number-pad' : 'default'
                }
                placeholder={accountFieldPlaceholder(currencyCode, refundKind)}
                maxLength={accountNumberMaxLength(currencyCode, refundKind)}
              />
              <Text variant="caption" style={{ color: colors.textMuted }}>
                Used only if your order fails. Pay from this account so a refund reaches you.
              </Text>
            </>
          )}

          <Button
            title="Continue"
            onPress={onConfirm}
            loading={creating}
            disabled={
              !venue ||
              !address ||
              !refundBank ||
              !isValidAccountNumber(currencyCode, refundAccount, refundKind) ||
              quoting ||
              creating
            }
            fullWidth
          />
          {walletReady && !address && (
            <Text variant="caption" style={{ color: colors.textMuted }}>
              Connect a wallet on this device first — your crypto is delivered straight to it.
            </Text>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      <PickerSheet
        visible={currencyPickerOpen}
        onClose={() => setCurrencyPickerOpen(false)}
        title="Currency"
        items={currencyOptions}
        selectedId={currencyCode}
        onSelect={(item) => {
          setCurrencyCode(item.id);
          // A new currency means a fresh route and refund bank — the ones
          // held over would either belong to the wrong currency's quote or
          // (for the refund bank) simply not exist in the new list.
          setAmount('');
          setVenue(null);
          setRefundAccount('');
          setCurrencyPickerOpen(false);
        }}
        emptyText="No currencies available."
      />
      <AssetPicker
        visible={tokenPickerOpen}
        side="to"
        title="Choose what to buy"
        // A buy is quoted per chain (want.chainId): the EVM chains in the
        // registry, plus Solana, which is delivered to her own Solana
        // address. A chain outside both could never be priced, so it is not
        // offered.
        allow={isBuyableChain}
        onSelect={async (token, chainId) => {
          // token.address is the real on-chain address (native coins
          // included) — exactly the (chainId, address) pair the server now
          // identifies the requested asset by, so no address resolution is
          // needed here.
          //
          // A Solana pick needs one more thing the EVM wallet cannot supply:
          // WHERE it lands. Nothing on Solana can be paid to a 20-byte
          // address, so without her Solana address there is no order to open
          // — say so and leave the choice unchanged rather than quoting a
          // route to a recipient that cannot exist.
          //
          // Deriving that address can also THROW (a locked or unreadable
          // key store), and an onSelect that rejects is an unhandled promise
          // rejection the user never sees — the sheet just sits there. A
          // failure to derive and a missing address are the same thing to
          // her, so they say the same thing.
          let destinationAddress: string | undefined;
          if (isSolanaLiFiChain(chainId)) {
            try {
              destinationAddress = (await getSolanaAddress(address ?? undefined)) ?? undefined;
            } catch {
              destinationAddress = undefined;
            }
            if (!destinationAddress) {
              Alert.alert(
                'Solana wallet not ready',
                'Set up your Solana wallet first, then choose a Solana token to buy.',
              );
              return;
            }
          }
          setWant({
            token: token.symbol.toUpperCase(),
            chainId,
            address: token.address,
            chain: chainKeyForPicked(chainId),
            destinationAddress,
          });
          setWantTokenMeta(token);
          setTokenPickerOpen(false);
        }}
        onClose={() => setTokenPickerOpen(false)}
      />
      <PickerSheet
        visible={refundOpen}
        onClose={() => setRefundOpen(false)}
        title="Refund account"
        items={banks.map((b) => ({ id: b.code, label: b.name }))}
        selectedId={refundBank?.code}
        searchable
        onSelect={(item) => {
          const next = banks.find((b) => b.code === item.id) ?? null;
          // Same reasoning as the sell screen's payout picker: a value typed
          // for one kind of destination is meaningless — and dangerously
          // plausible — against the other.
          if (!accountValueSurvivesKindChange(refundKind, next?.type)) {
            setRefundAccount('');
          }
          setRefundBank(next);
          setRefundOpen(false);
        }}
        emptyText="No banks available."
      />
    </SafeAreaView>
  );
}
