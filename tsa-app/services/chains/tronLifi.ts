// services/chains/tronLifi.ts
//
// Execute a LiFi swap whose SOURCE chain is Tron. A Tron route's
// transactionRequest.data is the whole TransactionRaw protobuf (hex) the
// aggregator built — the same "sign what you were given" shape as Sui and
// Solana, routed through the same backend prepare/sign/submit path a normal
// Tron send uses (services/chains/tronSend.ts).
//
// What makes signing somebody else's transaction safe is on the backend: it
// decodes it and refuses unless it is one plain contract call or transfer from
// this wallet, moving no more TRX than quoted, under a bounded fee limit — a
// permission-changing transaction never gets a digest (internal/chains/tron).
//
// A TRC-20 source needs the LiFi contract approved first — for exactly the
// trade's amount, never unlimited, so a bad quote or a future flaw in that
// contract can only ever reach this one trade's tokens. The backend reads the
// current allowance and answers with the next approval to sign: none when it
// already covers the trade; a reset to 0 when it is non-zero but short (USDT-
// style tokens refuse non-zero → non-zero); otherwise the exact amount. So
// this signs rounds until the backend has nothing left to ask for.
import { getNetwork } from '../../hooks/useNetwork';
import { SWAP_COPY } from '../submitErrors';
import { TRON_NATIVE_ADDRESS, type LiFiQuote } from '../lifi';
import { getTronAddress } from './tronWallet';
import { prepareTron, broadcastTron, type SendTronParams } from './tronSend';
import { getNonEVMTxStatus, registerNonEVMAddress } from './nonevmApi';

// Swaps are mainnet-only (LiFi has no testnets); see suiLifi.ts.
const SWAP_NETWORK = 'mainnet';
// Tron blocks are 3 s; an approval normally lands within a couple of them.
const APPROVAL_POLL_MS = 3_000;
const APPROVAL_TIMEOUT_MS = 90_000;
// Reset to 0, approve the amount, then the confirming "nothing to sign".
const MAX_APPROVAL_ROUNDS = 3;

export interface ExecuteLiFiTronSwapParams {
  quote: LiFiQuote;
  /** Base units the user authorized; the backend bounds the spend by this. */
  fromAmountRaw: string;
  fromSymbol: string;
  /** TRC-20 contract being sold, or LiFi's native-TRX placeholder. */
  fromTokenAddr?: string;
  /** EVM wallet id — used to look up the mnemonic + derived Tron address. */
  walletAddress: string;
  fee?: { feature: string; amountHuman: string; chainId?: number };
  onStage?: (label: string) => void;
  /** Injected in tests. */
  sleep?: (ms: number) => Promise<void>;
}

const defaultSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

async function waitForApproval(hash: string, sleep: (ms: number) => Promise<void>): Promise<void> {
  const deadline = Date.now() + APPROVAL_TIMEOUT_MS;
  while (Date.now() < deadline) {
    await sleep(APPROVAL_POLL_MS);
    let status;
    try {
      status = await getNonEVMTxStatus('tron', hash);
    } catch {
      continue; // a flaky status read is not a failed approval
    }
    if (status.found && status.confirmed) {
      if (!status.success) throw new Error(`Approval failed on-chain (tx ${hash}). Try again.`);
      return;
    }
  }
  throw new Error(`Approval is taking longer than expected (tx ${hash}). Try again once it confirms.`);
}

/**
 * Approve (when needed), then sign + broadcast a LiFi Tron-source swap.
 * Resolves with the swap's transaction id. Throws a user-facing message.
 */
export async function executeLiFiTronSwap({
  quote,
  fromAmountRaw,
  fromSymbol,
  fromTokenAddr,
  walletAddress,
  fee,
  onStage,
  sleep = defaultSleep,
}: ExecuteLiFiTronSwapParams): Promise<string> {
  if (getNetwork() === 'testnet') {
    throw new Error('Swaps are mainnet-only. Switch the app back to mainnet to swap.');
  }
  const rawHex = (quote.transactionRequest.data || '').replace(/^0x/i, '');
  if (!rawHex) {
    throw new Error('This route returned no transaction to sign. Refresh the quote and try again.');
  }

  const tronAddress = await getTronAddress(walletAddress);
  if (!tronAddress) throw new Error('No Tron key for this wallet.');
  // submit-tx only broadcasts for addresses registered to this user.
  await registerNonEVMAddress(walletAddress, 'tron', tronAddress).catch(() => {});

  const tokenAddr = fromTokenAddr && fromTokenAddr !== TRON_NATIVE_ADDRESS ? fromTokenAddr : '';
  const routeTarget = quote.transactionRequest.to ?? '';
  const base: SendTronParams = {
    from: tronAddress,
    to: tronAddress,
    amount: fromAmountRaw,
    symbol: fromSymbol,
    tokenAddr,
    walletAddress,
    network: SWAP_NETWORK,
    errorCopy: SWAP_COPY,
  };

  if (tokenAddr && quote.approvalAddress) {
    // The allowance goes only to the contract the swap itself calls; a quote
    // naming two different contracts is not one we can reason about.
    if (quote.approvalAddress !== routeTarget) {
      throw new Error('This route asks to approve a different contract than it calls. Refresh the quote and try again.');
    }
    const approval = { ...base, to: quote.approvalAddress, approve: true };
    let approved = false;
    for (let round = 0; round < MAX_APPROVAL_ROUNDS && !approved; round += 1) {
      onStage?.('Step 1 of 2: Approving token…');
      const { prepared } = await prepareTron(approval);
      if (prepared.payloads.length === 0) {
        approved = true;
        break;
      }
      // An approval moves no tokens; recording it at the trade's amount would
      // book a second outgoing transfer the user never made.
      const { txHash } = await broadcastTron(prepared, { ...approval, amount: '0' });
      onStage?.('Step 1 of 2: Waiting for approval to confirm…');
      await waitForApproval(txHash, sleep);
    }
    if (!approved) throw new Error('The token approval did not take effect. Try again.');
  }

  onStage?.(tokenAddr && quote.approvalAddress ? 'Step 2 of 2: Swapping…' : 'Swapping…');
  // `to` is the contract the route calls; the backend refuses a transaction
  // whose target differs from it, which pins what is signed to what was quoted.
  const swap = { ...base, to: routeTarget, tronRawHex: rawHex, fee };
  const { prepared } = await prepareTron(swap);
  const { txHash } = await broadcastTron(prepared, swap);
  return txHash;
}
