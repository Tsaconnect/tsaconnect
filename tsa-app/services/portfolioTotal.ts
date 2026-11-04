// services/portfolioTotal.ts
//
// The account's headline balance in USD: what Home shows as the total and
// Profile shows as "My Balance". It sums the same rows the Wallet tab lists
// (services/walletAssetSources.ts): every EVM chain, Tron, BTC, Solana, Sui,
// Spendable MCGP and discovered tokens, less the tokens the user hid. One
// implementation, so Home and the Wallet tab can never disagree.

import { getSignableWallet } from './wallet';
import { walletSnapshot } from './walletSnapshot';
import { getNetwork } from '../hooks/useNetwork';
import {
  combineSourceAssets,
  emptySourceAssets,
  type AssetSource,
} from '../utils/walletAssets';
import {
  listedIn,
  loadBtcAssets,
  loadEvmAssets,
  loadPremiumAssets,
  loadSolanaAssets,
  loadSuiAssets,
  loadTronAssets,
  readHiddenTokens,
  visibleUsdTotal,
  type WalletAsset,
} from './walletAssetSources';

/**
 * The total in USD, or null when it cannot be known — never zero for a failed
 * read, which would show a holder $0.00.
 *
 * A source that fails falls back to the rows the Wallet tab last saw for this
 * wallet. With none saved, a failed EVM read makes the total unknown (a
 * partial sum is a wrong number, not a smaller one); any other source counts
 * as none, as it does on the Wallet tab.
 */
export async function readPortfolioUsd(): Promise<number | null> {
  // Balances for the wallet this device holds a key for. Without one the
  // backend would answer for the account's REGISTERED address, and the total
  // would show funds the user cannot touch on this device — so no key means
  // only the account's Spendable MCGP, which is not tied to a device.
  const signer = await getSignableWallet();
  if (!signer) {
    const premium = await loadPremiumAssets([]).catch(() => null);
    return premium ? premium.reduce((s, a) => s + a.usdValue, 0) : null;
  }

  // Local reads run alongside the network fan-out, not in front of it.
  const savedRead = walletSnapshot<WalletAsset>(signer, getNetwork())
    .read()
    .then((s) => s?.value ?? null)
    .catch(() => null);
  const hiddenRead = readHiddenTokens();

  // Icons and names are not needed for a sum, so no token list is passed.
  const loaders: Record<AssetSource, () => Promise<WalletAsset[]>> = {
    evm: () => loadEvmAssets([], signer),
    tron: () => loadTronAssets(signer, []),
    btc: () => loadBtcAssets(signer),
    solana: () => loadSolanaAssets(signer, []),
    sui: () => loadSuiAssets(signer, []),
    premium: () => loadPremiumAssets([]),
  };
  const sources = emptySourceAssets<WalletAsset>();
  let unknown = false;
  await Promise.all(
    (Object.keys(loaders) as AssetSource[]).map(async (source) => {
      try {
        sources[source] = await loaders[source]();
      } catch {
        const last = (await savedRead)?.sources[source];
        if (last) sources[source] = last;
        // The EVM fan-out holds most of what users own: without it the total
        // is unknown. Any other source that cannot be read counts as none,
        // exactly as the Wallet tab counts it (a chain the backend has not
        // configured also reads as failed).
        else if (source === 'evm') unknown = true;
        else sources[source] = [];
      }
    }),
  );
  if (unknown) return null;
  const [saved, hidden] = await Promise.all([savedRead, hiddenRead]);

  // Discovery takes up to 25 s, so it is not awaited here: the discovered rows
  // the Wallet tab last saw count, unless a source now reports that token
  // itself or the user imported it (imported rows carry no price, so they add
  // nothing, but they still shadow a discovered copy, as on the Wallet tab).
  const base = combineSourceAssets(sources);
  const imported = (saved?.extras ?? []).filter((a) => !a.discovered);
  const listed = listedIn([...base, ...imported]);
  const discovered = (saved?.extras ?? []).filter(
    (a) => a.discovered && !listed(a.details?.chainKey ?? '', a.symbol, a.contractAddress),
  );

  return visibleUsdTotal([...base, ...discovered], hidden);
}
