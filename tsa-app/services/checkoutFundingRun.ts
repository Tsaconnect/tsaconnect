// services/checkoutFundingRun.ts
//
// Paying a marketplace checkout with any token she holds (owner decision
// 2026-09-29). Two halves, run here in order:
//
//   1. one LiFi swap, signed by her, lands the order token (Sonic USDC/USDT) in
//      HER OWN wallet on Sonic mainnet — sized by the server from the orders it
//      holds, less what she already has there;
//   2. the unchanged escrow flow (approve + createOrder, services/serviceCheckout
//      escrowOrder) pays each order from that wallet.
//
// ProductEscrowV2 records msg.sender as the buyer, which is why the funds must
// land in her wallet and the escrow be paid from it. It is also what makes this
// safe to interrupt: whatever becomes of the funding, the swap pays her own
// wallet — at worst she ends up holding USDC and pays with it directly.
//
// What must survive a kill is persisted (one record, FUNDING_RUN_KEY): the
// checkout group and its orders, the intent, the swap's hash (also written by
// executeSellSwap BEFORE the broadcast), which orders are already paid and each
// order's escrow progress. Opening checkout again resumes from it (resume()).
//
// One run per app: the runner is a module singleton the screen subscribes to,
// so she can leave the screen mid-bridge (some take ~20 minutes) and come back
// to the same run rather than start a second one.
//
// Everything that talks to the network is injected (FundingDeps) so the state
// machine is a plain test.
import AsyncStorage from '@react-native-async-storage/async-storage';
import type { LiFiToken } from '@/services/lifi';
import type { Order } from '@/services/orderApi';
import type { EscrowProgress } from '@/services/serviceCheckout';
import type { SellSwapRun } from '@/services/sellSwapRun';
import {
  FUNDING_CODES,
  type FundingConfirm,
  type FundingIntent,
  type FundingSource,
  type FundingStart,
} from '@/services/checkoutFundingApi';

/** The order tokens a checkout is paid in, on Sonic mainnet (tsa-api-go sonic preset). */
export const SONIC_ORDER_TOKENS: Record<string, string> = {
  USDC: '0x29219dd400f2bf60e5a23d13be72b486d4038894',
  USDT: '0x6047828dc181963ba44974801ff68e538da5eaf9',
};
export const SONIC_CHAIN_ID = 146;

export const FUNDING_RUN_KEY = 'checkout.funding.run.v1';

/** How often the server is asked whether the funds have landed. */
export const DELIVERY_POLL_MS = 10_000;
/** The least time the screen waits on a bridge before handing back ("come back later"). */
const MIN_WAIT_MS = 30 * 60_000;
const REPORT_TRIES = 3;
const REPORT_DELAY_MS = 2_000;
/** Consecutive unreadable answers (not "still swapping") before the wait is handed back. */
const MAX_ERRORS = 5;

export type FundingStage =
  | 'idle'
  | 'creating_orders'
  | 'starting'
  | 'signing'
  | 'bridging'
  | 'sonic_gas'
  | 'paying'
  | 'done'
  | 'failed';

/**
 * How a failure stands:
 * - 'retry'    nothing is in flight: pay again (the orders are still waiting), or the
 *              funds are in her wallet and "Continue" pays the escrow;
 * - 'refunded' the swap ended short (LiFi refunded it, or it failed): her tokens are
 *              with her, and the orders will simply expire;
 * - 'expired'  the funds landed but an order expired before its escrow: terminal — the
 *              money is in her wallet on Sonic, and she checks out again;
 * - 'wait'     the swap may still be on its way: come back and Continue;
 * - 'held'     it may have been sent and we cannot find its hash: check the wallet.
 */
export type FundingOutcome = 'retry' | 'refunded' | 'expired' | 'wait' | 'held';

/**
 * Whether leaving this run (View orders, Back to checkout) may forget it on the
 * phone. Only when nothing is in flight: done, ended short or expired, or a
 * retry with no swap sent and no funds delivered. Any other run keeps its
 * persisted record and hash, so checkout picks it up again.
 */
export function canForgetRun(s: FundingRunState): boolean {
  if (s.stage === 'done' || s.stage === 'idle') return true;
  if (s.stage !== 'failed') return false;
  if (s.outcome === 'refunded' || s.outcome === 'expired') return true;
  const rec = s.record;
  return s.outcome === 'retry' && (!rec || (!rec.intentId && !rec.swapHash && !rec.delivered));
}

export type FundingRecord = {
  v: 1;
  groupId: string;
  orders: Order[];
  /** What she pays with. */
  token: LiFiToken;
  chainId: number;
  wallet: string;
  intentId?: string;
  swapHash?: string;
  etaSeconds?: number;
  startedAt: number;
  delivered?: boolean;
  gasDone?: boolean;
  paid: string[];
  progress: Record<string, EscrowProgress>;
};

export type FundingRunState = {
  stage: FundingStage;
  error?: string;
  outcome?: FundingOutcome;
  record?: FundingRecord;
  /** Which order the escrow is on, while paying. */
  orderIndex?: number;
  /** The escrow's own step, while paying. */
  escrowPhase?: string;
};

export type FundingDeps = {
  start: (groupId: string, src: FundingSource) => Promise<FundingStart>;
  record: (intentId: string, hash: string) => Promise<void>;
  confirm: (intentId: string) => Promise<FundingConfirm>;
  get: (intentId: string) => Promise<FundingIntent | null>;
  abandon: (intentId: string) => Promise<void>;
  sourceGas: (intentId: string) => Promise<unknown>;
  sonicGas: (intentId: string) => Promise<unknown>;
  executeSwap: (run: SellSwapRun) => Promise<string>;
  pendingHash: (intentId: string) => Promise<string | null>;
  clearPendingHash: (intentId: string) => Promise<void>;
  escrow: (order: Order, onPhase: (phase: string) => void, progress: EscrowProgress) => Promise<Order>;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
  storage?: { get: (k: string) => Promise<string | null>; set: (k: string, v: string) => Promise<void>; remove: (k: string) => Promise<void> };
};

export const FUNDING_COPY = {
  refunded: "The swap didn't complete, so nothing was paid — your tokens are in, or on their way back to, your wallet. These orders will expire; you can check out again.",
  partial: 'The swap delivered a different token than quoted — check your wallet, then contact support and we will help you finish.',
  stillOnItsWay: "Your swap is still on its way. Some bridges take up to about 20 minutes — come back to checkout and it will pick up where it stopped. Don't pay again meanwhile.",
  notConfirmed: "Your swap was sent, but we couldn't tell our server yet. Open checkout again in a minute to carry on — don't pay again meanwhile.",
  broadcastUnconfirmed: "We couldn't confirm your swap was sent. Check your wallet's activity before paying again.",
  notSent: "Your payment wasn't sent. Your orders are still waiting — you can pay again.",
  waiting: 'Your orders are waiting for payment.',
  closed: "Your swap was sent after this payment had closed. The funds will still arrive in your wallet on Sonic — pay with USDC or USDT once they land.",
  expired: 'Your funds are in your wallet on Sonic, but some orders expired before they could be paid. Check out again to pay for them — nothing was taken for the expired orders.',
} as const;

const isNamed = (e: unknown, name: string): e is Error & Record<string, any> =>
  !!e && typeof e === 'object' && (e as Error).name === name;
const codeOf = (e: unknown): string | undefined => (e as { code?: string } | null)?.code;
const statusOf = (e: unknown): number | undefined => {
  const s = (e as { status?: unknown } | null)?.status;
  return typeof s === 'number' ? s : undefined;
};
const messageOf = (e: unknown, fallback: string) => (e instanceof Error && e.message ? e.message : fallback);

/** "about 18 minutes" / "a few seconds" — the route's own estimate, said honestly. */
export function etaLabel(seconds?: number): string {
  if (!seconds || seconds <= 0) return 'a few minutes';
  if (seconds < 60) return 'under a minute';
  const min = Math.round(seconds / 60);
  return min <= 1 ? 'about a minute' : `about ${min} minutes`;
}

export class CheckoutFundingRunner {
  private state: FundingRunState = { stage: 'idle' };
  private listeners = new Set<(s: FundingRunState) => void>();
  private running = false;

  constructor(private deps: FundingDeps) {}

  getState = (): FundingRunState => this.state;

  subscribe = (listener: (s: FundingRunState) => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  isRunning = () => this.running;

  private set(next: FundingRunState) {
    this.state = next;
    for (const l of this.listeners) l(next);
  }

  private stage(stage: FundingStage, rec?: FundingRecord, extra: Partial<FundingRunState> = {}) {
    this.set({ stage, record: rec ?? this.state.record, ...extra });
  }

  private fail(error: string, outcome: FundingOutcome, rec?: FundingRecord) {
    this.set({ stage: 'failed', error, outcome, record: rec ?? this.state.record, orderIndex: this.state.orderIndex });
  }

  // ─── persistence ─────────────────────────────────────────────────────────

  private store() {
    return this.deps.storage ?? {
      get: (k: string) => AsyncStorage.getItem(k),
      set: (k: string, v: string) => AsyncStorage.setItem(k, v),
      remove: (k: string) => AsyncStorage.removeItem(k),
    };
  }

  /** The run this phone has in progress, or null. An unreadable record is dropped: it names nothing we could act on. */
  async load(): Promise<FundingRecord | null> {
    try {
      const raw = await this.store().get(FUNDING_RUN_KEY);
      if (!raw) return null;
      const rec = JSON.parse(raw) as FundingRecord;
      return rec?.v === 1 && typeof rec.groupId === 'string' ? rec : null;
    } catch {
      return null;
    }
  }

  private async save(rec: FundingRecord) {
    try {
      await this.store().set(FUNDING_RUN_KEY, JSON.stringify(rec));
    } catch (e) {
      console.warn('[checkout-funding] could not persist the run', e);
    }
  }

  private async clear() {
    await this.store().remove(FUNDING_RUN_KEY).catch(() => {});
  }

  // ─── the run ─────────────────────────────────────────────────────────────

  /**
   * Create the checkout's orders (the caller's createOrders, with fundWithSwap)
   * and fund them. `pricedFromAmount` is what she was shown and agreed to (the
   * cart quote's fromAmount): the route signed is held to it (sellSwapRun's
   * price guard, 2%).
   */
  async pay(args: {
    token: LiFiToken;
    chainId: number;
    wallet: string;
    pricedFromAmount: string;
    /** Create the checkout's orders (fundWithSwap) and name their group. */
    createOrders: () => Promise<{ orders: Order[]; groupId: string }>;
  }): Promise<void> {
    if (this.running) return;
    this.running = true;
    // A run this phone already holds (a resume that has not loaded it yet) is
    // followed, never shadowed by a second one.
    if (await this.load()) {
      this.running = false;
      await this.resume();
      return;
    }
    try {
      this.stage('creating_orders', undefined, { record: undefined });
      let created;
      try {
        created = await args.createOrders();
      } catch (e) {
        this.fail(messageOf(e, 'Your orders could not be created.'), 'retry', undefined);
        return;
      }
      const rec: FundingRecord = {
        v: 1, groupId: created.groupId, orders: created.orders, token: args.token, chainId: args.chainId,
        wallet: args.wallet, startedAt: this.deps.now(), paid: [], progress: {},
      };
      await this.save(rec);
      await this.fund(rec, args.pricedFromAmount);
    } finally {
      this.running = false;
    }
  }

  /**
   * Pay again for a checkout whose orders exist but whose funding was never
   * sent (it was refused, the price moved, or the app died before signing).
   * `pricedFromAmount` comes from a fresh group quote she confirmed.
   */
  async retry(pricedFromAmount: string): Promise<void> {
    if (this.running) return;
    const rec = this.state.record ?? (await this.load());
    if (!rec || rec.intentId || rec.swapHash) return;
    this.running = true;
    try {
      await this.fund(rec, pricedFromAmount);
    } finally {
      this.running = false;
    }
  }

  private src(rec: FundingRecord): FundingSource {
    return { sourceChainId: rec.chainId, sourceToken: rec.token.address, walletAddress: rec.wallet };
  }

  private async fund(rec: FundingRecord, pricedFromAmount: string) {
    this.stage('starting', rec);
    let started: FundingStart;
    try {
      started = await this.deps.start(rec.groupId, this.src(rec));
    } catch (e) {
      this.fail(messageOf(e, "Paying with this token couldn't be started."), 'retry', rec);
      return;
    }
    rec.intentId = started.intent.id;
    rec.etaSeconds = started.priced?.etaSeconds;
    await this.save(rec);

    this.stage('signing', rec);
    let hash: string | null | undefined;
    try {
      hash = await this.deps.executeSwap({
        intentId: started.intent.id,
        quote: started.quote,
        gasTopUp: started.gasTopUp,
        token: rec.token,
        sourceChainId: rec.chainId,
        sourceAddress: rec.wallet,
        pricedFromAmount,
        closeIntent: this.deps.abandon,
        requestGasTopUp: this.deps.sourceGas,
        network: 'mainnet',
      });
    } catch (e) {
      if (isNamed(e, 'SwapBroadcastUnconfirmedError')) {
        // It may be on its way: never a retry — follow the hash if we have it.
        hash = (e.txHash as string | undefined) ?? (await this.deps.pendingHash(started.intent.id));
        if (!hash) {
          this.fail(FUNDING_COPY.broadcastUnconfirmed, 'held', rec);
          return;
        }
      } else {
        // Refused before or at the node: nothing left her wallet, and
        // executeSwap closed the intent. The orders still wait for payment.
        rec.intentId = undefined;
        await this.save(rec);
        this.fail(messageOf(e, FUNDING_COPY.notSent), 'retry', rec);
        return;
      }
    }
    rec.swapHash = hash;
    await this.save(rec);
    await this.afterSwap(rec);
  }

  /** Tell the server the hash, wait for the funds to land, then pay the escrows. */
  private async afterSwap(rec: FundingRecord) {
    const reported = await this.report(rec);
    if (reported === 'rejected') {
      // The intent was closed before the hash reached it. The swap still pays
      // her own wallet: nothing more to follow here.
      await this.clear();
      this.fail(FUNDING_COPY.closed, 'refunded', rec);
      return;
    }
    if (reported === 'unreachable') {
      this.fail(FUNDING_COPY.notConfirmed, 'wait', rec);
      return;
    }
    const confirmed = await this.waitDelivery(rec);
    if (!confirmed) return;
    rec.delivered = true;
    await this.save(rec);
    await this.payEscrows(rec, confirmed);
  }

  private async report(rec: FundingRecord): Promise<'reported' | 'rejected' | 'unreachable'> {
    const id = rec.intentId!;
    const hash = rec.swapHash!;
    for (let i = 0; i < REPORT_TRIES; i++) {
      if (i > 0) await this.deps.sleep(REPORT_DELAY_MS);
      try {
        await this.deps.record(id, hash);
        return 'reported';
      } catch (e) {
        const status = statusOf(e);
        if (status !== undefined && status >= 400 && status < 500 && ![401, 403, 408, 429].includes(status)) {
          // The server's final word — unless it already holds this very hash.
          const now = await this.deps.get(id).catch(() => null);
          if (now?.swapTxHash && now.swapTxHash.toLowerCase() === hash.toLowerCase()) return 'reported';
          return 'rejected';
        }
      }
    }
    return 'unreachable';
  }

  private async waitDelivery(rec: FundingRecord): Promise<FundingConfirm | null> {
    const id = rec.intentId!;
    this.stage('bridging', rec);
    const waitMs = Math.max(MIN_WAIT_MS, (rec.etaSeconds ?? 0) * 3_000);
    const deadline = this.deps.now() + waitMs;
    let errors = 0;
    for (let first = true; first || this.deps.now() < deadline; first = false) {
      if (!first) await this.deps.sleep(DELIVERY_POLL_MS);
      try {
        return await this.deps.confirm(id);
      } catch (e) {
        const code = codeOf(e);
        if (code === FUNDING_CODES.swapPending) {
          errors = 0;
          continue;
        }
        if (code === FUNDING_CODES.swapFailed || code === FUNDING_CODES.swapRefunded || code === FUNDING_CODES.swapPartial) {
          await this.finish(rec);
          this.fail(code === FUNDING_CODES.swapPartial ? FUNDING_COPY.partial : FUNDING_COPY.refunded, 'refunded', rec);
          return null;
        }
        if (++errors >= MAX_ERRORS) break;
      }
    }
    this.fail(FUNDING_COPY.stillOnItsWay, 'wait', rec);
    return null;
  }

  /** The funds are in her wallet: S for the escrow txs if she needs it, then each order's escrow. */
  private async payEscrows(rec: FundingRecord, confirmed: FundingConfirm) {
    const id = rec.intentId!;
    if (confirmed.needsGas && !rec.gasDone) {
      this.stage('sonic_gas', rec);
      try {
        await this.deps.sonicGas(id);
      } catch (e) {
        this.fail(messageOf(e, "We couldn't cover the network fee on Sonic. Your funds are in your wallet — add a little S, then tap Continue."), 'retry', rec);
        return;
      }
      rec.gasDone = true;
      await this.save(rec);
    }
    const expired: string[] = [];
    for (let i = 0; i < rec.orders.length; i++) {
      const order = rec.orders[i];
      if (rec.paid.includes(order.id)) continue;
      this.stage('paying', rec, { orderIndex: i });
      const progress = (rec.progress[order.id] ??= {});
      try {
        await this.deps.escrow(order, (phase) => this.stage('paying', rec, { orderIndex: i, escrowPhase: phase }), progress);
      } catch (e) {
        if (isNamed(e, 'OrderNotPayableError')) {
          // It can never be paid (expired or cancelled): not a retry — go on
          // with the others, and end the run on it below.
          expired.push(order.id);
          continue;
        }
        await this.save(rec);
        this.fail(`${messageOf(e, 'The payment did not go through.')} Your funds are in your wallet — tap Continue to try again.`, 'retry', rec);
        return;
      }
      rec.paid.push(order.id);
      await this.save(rec);
    }
    await this.finish(rec);
    if (expired.length > 0) {
      this.fail(FUNDING_COPY.expired, 'expired', rec);
      return;
    }
    this.stage('done', rec);
  }

  /** The run is over: forget it on this phone. */
  private async finish(rec: FundingRecord) {
    await this.clear();
    if (rec.intentId) await this.deps.clearPendingHash(rec.intentId).catch(() => {});
  }

  /**
   * Pick up a run this phone left: after a kill, on reopening checkout, or on
   * Continue. Signs nothing new: it reports a hash already on the phone, waits
   * for the funds, and pays the escrows from her wallet.
   */
  async resume(): Promise<void> {
    if (this.running) return;
    // Claimed before the (async) load, so a Pay tapped meanwhile waits on it.
    this.running = true;
    const rec = await this.load();
    if (!rec) {
      this.running = false;
      if (this.state.stage !== 'idle' && this.state.stage !== 'done') this.set({ stage: 'idle' });
      return;
    }
    try {
      if (rec.delivered) {
        this.stage('bridging', rec);
        const confirmed = await this.waitDelivery(rec);
        if (confirmed) await this.payEscrows(rec, confirmed);
        return;
      }
      if (!rec.swapHash && rec.intentId) {
        rec.swapHash = (await this.deps.pendingHash(rec.intentId)) ?? undefined;
        if (!rec.swapHash) {
          const intent = await this.deps.get(rec.intentId).catch(() => null);
          if (intent?.swapTxHash) {
            rec.swapHash = intent.swapTxHash;
          } else if (!intent || intent.status === 'swapping' || intent.status === 'failed') {
            // Never signed on this phone (the hash is written before any
            // broadcast): close it so the orders can be paid again.
            if (intent?.status === 'swapping') await this.deps.abandon(rec.intentId).catch(() => {});
            rec.intentId = undefined;
            await this.save(rec);
            this.fail(FUNDING_COPY.notSent, 'retry', rec);
            return;
          }
        }
        if (rec.swapHash) await this.save(rec);
      }
      if (rec.swapHash && rec.intentId) {
        await this.afterSwap(rec);
        return;
      }
      this.fail(FUNDING_COPY.waiting, 'retry', rec);
    } finally {
      this.running = false;
    }
  }

  /**
   * Forget a finished or abandoned run (the orders, if unpaid, simply expire).
   * Refused while anything may be in flight (canForgetRun): the record and the
   * swap's hash are what let checkout follow it again.
   */
  async discard(): Promise<void> {
    if (this.running || !canForgetRun(this.state)) return;
    const rec = this.state.record ?? (await this.load());
    if (rec?.intentId && !rec.swapHash) await this.deps.abandon(rec.intentId).catch(() => {});
    if (rec) await this.finish(rec);
    this.set({ stage: 'idle' });
  }
}
