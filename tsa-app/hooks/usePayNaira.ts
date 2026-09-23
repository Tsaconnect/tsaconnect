// hooks/usePayNaira.ts
//
// Paying a TSA Pay merchant that settles in naira, with any token she holds.
//
// One route is chosen by the ramp's own quote, never by us:
//   - Paycrest fills the token as it is (a stablecoin on a live route): open
//     the payment's payout order, fund it with the sponsored deposit, claim it;
//   - anything else: the swapped-sell pipeline (hooks/useSwappedSell) run with
//     the PAYMENT as its intent — swap into USDC on Base, open the payout for
//     what landed, send it. That pipeline is injected, not copied.
//
// Nothing reaches the server until she authorizes (usePayAuthorization). A 409
// `pay_in_progress` means an attempt of hers already exists: it is resumed,
// never duplicated. Codes are keyed on, never sentences.
import { useCallback, useRef, useState } from 'react';
import { getChainByChainId } from '@/constants/chains';
import { usePayAuthorization, type AuthorizationProps } from '@/hooks/usePayAuthorization';
import { useSignableWallet } from '@/hooks/useSignableWallet';
import {
  useSwappedSell, type CreateIntent, type SellStage, type StartSwapArgs,
} from '@/hooks/useSwappedSell';
import { getSponsoredRoutes, isGasSponsored } from '@/services/gasSponsorship';
import { isNonEVMLiFiChain, type LiFiToken } from '@/services/lifi';
import {
  claimPayment, getQuote, isConvertedVenue, selectVenue, type PaycrestOrder, type PaycrestVenue,
} from '@/services/paycrestApi';
import {
  checkFunding, fundingMessage, nativeBalanceFor, sendSponsoredPaycrestDeposit,
} from '@/services/paycrestDeposit';
import { getOpenSellIntent } from '@/services/paycrestSellSwap';
import { getPayQuote, payConverted, payDirect, PayError, type PayCheckout } from '@/services/payApi';
import { getWalletBalances, normalizeWalletBalances, type WalletBalanceWithChain } from '@/services/walletApi';
import { nairaFromKobo, payAmountLabel } from '@/utils/payCheckout';

export const PAY_IN_PROGRESS = 'pay_in_progress';

export type PayNairaStage = 'idle' | 'paying' | 'shortfall' | 'waiting' | 'failed';

export type UsePayNaira = {
  stage: PayNairaStage;
  /** With `shortfall`: USDC (whole units) the swap landed short by. */
  missingUsdc?: string;
  error?: string;
  /** The server's machine code behind `error`, when it sent one (e.g. KYC). */
  errorCode?: string;
  /** The swapped-sell pipeline's own stage, for progress copy while `paying`. */
  swapStage: SellStage;
  /** A direct payment's payout order — reachable from the order screen if the send failed. */
  order?: PaycrestOrder;
  authorization: AuthorizationProps;
  payWith(token: LiFiToken, chainId: number): void;
  /** What she will send, e.g. "0.084 BNB" — an estimate, before she taps Pay. */
  estimate(token: LiFiToken, chainId: number): Promise<string>;
  completeWithWallet(): Promise<void>;
  /** Pick up her own attempt at this payment, e.g. after the app was closed mid-swap. */
  resume(): Promise<void>;
};

type Pick = { token: LiFiToken; chainId: number };
type Direct = { stage: 'paying' | 'waiting' | 'failed'; error?: string; errorCode?: string; order?: PaycrestOrder };

const messageOf = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);
const codeOf = (e: unknown) => (e instanceof PayError ? e.code : undefined);

/**
 * The venue Paycrest fills as it is for `token` on `chainId` when the merchant
 * is owed `merchantNetKobo` — or null, meaning the token must be swapped first.
 * A converted venue is Paycrest's own conversion, never what a payment uses.
 */
async function directVenue(merchantNetKobo: number, token: LiFiToken, chainId: number): Promise<PaycrestVenue | null> {
  const quote = await getQuote('sell', nairaFromKobo(merchantNetKobo));
  const chain = getChainByChainId(chainId)?.key;
  const venue = selectVenue(quote.venues, { token: token.symbol, chainId, address: token.address, chain });
  return venue && !isConvertedVenue(venue) ? venue : null;
}

export function usePayNaira(checkout: PayCheckout): UsePayNaira {
  const { address } = useSignableWallet();
  const [direct, setDirect] = useState<Direct | null>(null);
  const [createCode, setCreateCode] = useState<string | undefined>();
  const inProgress = useRef(false);

  // The payment's own intent, through the swapped-sell pipeline. Refusals are
  // remembered by code: the pipeline reports only their words.
  const createIntent: CreateIntent = async (a) => {
    try {
      return await payConverted(checkout.id, {
        sourceChainId: a.sourceChainId,
        sourceToken: a.token.address,
        sourceAddress: a.sourceAddress,
      });
    } catch (e) {
      if (codeOf(e) === PAY_IN_PROGRESS) inProgress.current = true;
      else setCreateCode(codeOf(e));
      throw e;
    }
  };
  const swapped = useSwappedSell({ createIntent });

  /** An attempt of hers already exists: follow it. No open sale means a direct order, which the server settles. */
  const resumeOpen = useCallback(async () => {
    if (!address) return;
    let open;
    try {
      open = await getOpenSellIntent();
    } catch (e) {
      setDirect({ stage: 'failed', error: messageOf(e, 'Could not check on your payment. Please try again.') });
      return;
    }
    if (open) {
      setDirect(null);
      await swapped.resume(open, address);
    } else {
      setDirect({ stage: 'waiting' });
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps -- swapped's callbacks are stable.
  }, [address, swapped.resume]);

  const payDirectly = async (venue: PaycrestVenue, wallet: string) => {
    let balances: WalletBalanceWithChain[] | null = null;
    try {
      const res = await getWalletBalances(undefined, wallet);
      balances = res.success ? normalizeWalletBalances(res.data) : null;
    } catch {
      balances = null; // a flaky indexer must not block a payment she can fund
    }
    const routes = await getSponsoredRoutes().catch(() => []);
    const funding = checkFunding(venue, balances, { gasSponsored: isGasSponsored(routes, venue.chain, venue.token) });
    if (!funding.ok) {
      setDirect({ stage: 'failed', error: fundingMessage(funding, 'payment') ?? 'This payment cannot be funded.' });
      return;
    }

    const order = await payDirect(checkout.id, { chainKey: venue.chain, token: venue.token });
    setDirect({ stage: 'paying', order });
    try {
      await sendSponsoredPaycrestDeposit(order, wallet, nativeBalanceFor(order.chain, balances));
    } catch (e) {
      setDirect({ stage: 'failed', error: messageOf(e, 'Could not send. Please try again.'), order });
      return;
    }
    // The deposit already left; a lost claim only degrades to the ordinary
    // unmatched-deposit path, so it never fails the payment.
    await claimPayment(order.id).catch((e) => console.warn('[pay] claimPayment failed', e));
    setDirect({ stage: 'waiting', order });
  };

  const run = async ({ token, chainId }: Pick) => {
    if (!address) return;
    setCreateCode(undefined);
    inProgress.current = false;
    if (isNonEVMLiFiChain(chainId)) {
      setDirect({
        stage: 'failed',
        error: `Paying with ${token.symbol} from that network isn't supported yet. Pick a coin on another network.`,
      });
      return;
    }
    setDirect({ stage: 'paying' });
    try {
      const venue = await directVenue(checkout.merchantNet, token, chainId);
      if (venue) {
        await payDirectly(venue, address);
      } else {
        setDirect(null);
        const args: StartSwapArgs = {
          sourceChainId: chainId,
          token: { ...token, chainId },
          walletAddress: address,
          sourceAddress: address,
        };
        await swapped.start(args);
        if (inProgress.current) {
          inProgress.current = false;
          await resumeOpen();
        }
      }
    } catch (e) {
      if (codeOf(e) === PAY_IN_PROGRESS) {
        await resumeOpen();
      } else {
        setDirect({ stage: 'failed', error: messageOf(e, 'Something went wrong. Please try again.'), errorCode: codeOf(e) });
      }
    }
  };

  const { request, authorization } = usePayAuthorization<Pick>({
    title: () => 'Authorize payment',
    onAuthorized: (pick) => { void run(pick); },
  });

  const payWith = useCallback((token: LiFiToken, chainId: number) => request({ token, chainId }), [request]);

  const estimate = async (token: LiFiToken, chainId: number): Promise<string> => {
    const venue = await directVenue(checkout.merchantNet, token, chainId);
    if (venue) {
      // Stablecoins: to the cent, upward — an "about" figure never understates.
      const amount = Math.ceil(venue.tokenAmount * 100) / 100;
      return `${amount.toLocaleString('en-US', { maximumFractionDigits: 2 })} ${venue.token}`;
    }
    if (!address) return '';
    const q = await getPayQuote(checkout.id, {
      sourceChainId: chainId,
      sourceToken: token.address,
      sourceAddress: address,
    });
    return payAmountLabel(q.fromAmount, token.decimals, token.symbol);
  };

  const completeWithWallet = useCallback(async () => {
    if (swapped.intent) await swapped.completeWithWallet(swapped.intent);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [swapped.intent, swapped.completeWithWallet]);

  const fromSwap: PayNairaStage =
    swapped.stage === 'idle' ? 'idle'
      : swapped.stage === 'shortfall' ? 'shortfall'
        : swapped.stage === 'done' ? 'waiting'
          : swapped.stage === 'failed' ? 'failed'
            : 'paying';
  const stage = direct?.stage ?? fromSwap;

  return {
    stage,
    missingUsdc: stage === 'shortfall' ? swapped.missingUsdc : undefined,
    error: direct?.error ?? (stage === 'failed' ? swapped.error : undefined),
    errorCode: direct?.errorCode ?? (stage === 'failed' ? createCode : undefined),
    swapStage: swapped.stage,
    order: direct?.order ?? swapped.order,
    authorization,
    payWith,
    estimate,
    completeWithWallet,
    resume: resumeOpen,
  };
}
