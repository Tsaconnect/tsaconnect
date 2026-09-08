// services/chains/suiLifi.ts
//
// Execute a LiFi swap whose SOURCE chain is Sui. A Sui route's
// transactionRequest carries `data` and nothing else: the whole BCS
// TransactionData the aggregator built, base64-encoded — the same shape Solana
// uses, and unlike Bitcoin, where the route describes a deposit we assemble
// ourselves.
//
// So this signs a transaction somebody else wrote. What makes that safe is on
// the backend: it dry-runs the transaction against a Sui node first and refuses
// unless the simulation succeeds, every outflow belongs to this wallet, and no
// coin type leaves by more than the amount quoted (internal/chains/sui,
// prepareFromTxBytes). Only then does it hand back a digest to sign.
import { getNetwork } from '../../hooks/useNetwork';
import { getSuiAddress } from './suiWallet';
import { prepareSui, broadcastSui } from './suiSend';
import { registerNonEVMAddress } from './nonevmApi';

// Swaps are mainnet-only (LiFi has no testnets), so every leg is pinned to
// mainnet rather than following the app's network toggle — the address, the
// signing key and the broadcast all have to agree.
const SWAP_NETWORK = 'mainnet';

export interface ExecuteLiFiSuiSwapParams {
  /** quote.transactionRequest.data — base64 BCS TransactionData from LiFi. */
  txBytesB64: string;
  /** Base units the user authorized; the backend bounds the spend by this. */
  fromAmountRaw: string;
  fromSymbol: string;
  /**
   * Coin type of the token being sold ("0x…::usdc::USDC"; empty for native
   * SUI). The backend needs it to know WHICH coin `fromAmountRaw` bounds:
   * gas is paid in SUI and shows up as a second outflow, so without this the
   * two get measured against the same ceiling and every swap is refused.
   */
  fromTokenAddr?: string;
  /** EVM wallet id — used to look up the mnemonic + derived Sui address. */
  walletAddress: string;
  /** Fee bucket + human-units amount; see SendSuiParams.fee. */
  fee?: { feature: string; amountHuman: string; chainId?: number };
  onStage?: (label: string) => void;
}

/**
 * Sign + broadcast a LiFi Sui-source swap. Resolves with the transaction
 * digest. Throws with a user-facing message on failure.
 */
export async function executeLiFiSuiSwap({
  txBytesB64,
  fromAmountRaw,
  fromSymbol,
  fromTokenAddr,
  walletAddress,
  fee,
  onStage,
}: ExecuteLiFiSuiSwapParams): Promise<string> {
  if (getNetwork() === 'testnet') {
    throw new Error('Swaps are mainnet-only. Switch the app back to mainnet to swap.');
  }
  if (!txBytesB64) {
    throw new Error('This route returned no transaction to sign. Refresh the quote and try again.');
  }

  const suiAddress = await getSuiAddress(walletAddress);
  if (!suiAddress) throw new Error('No Sui key for this wallet.');

  // submit-tx only broadcasts for addresses registered to this user;
  // registration is an idempotent upsert, so make sure it happened even if the
  // user never opened the wallet's Sui view.
  await registerNonEVMAddress(walletAddress, 'sui', suiAddress).catch(() => {});

  const params = {
    from: suiAddress,
    // The route decides its own recipients; `to` is unused on this path and the
    // backend does not read it. The amount is what bounds the spend.
    to: suiAddress,
    amount: fromAmountRaw,
    symbol: fromSymbol,
    tokenAddr: fromTokenAddr ?? '',
    walletAddress,
    txBytesB64,
    network: SWAP_NETWORK,
    fee,
  };

  onStage?.('Checking the route…');
  const { prepared } = await prepareSui(params);

  onStage?.('Signing swap…');
  const { txHash } = await broadcastSui(prepared, params);
  return txHash;
}
