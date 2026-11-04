// services/walletAssetSources.ts
//
// Where every wallet row comes from: the EVM fan-out, each non-EVM chain, the
// Spendable MCGP entitlement, and the custom-token / discovery merges. The
// Wallet tab renders these rows; Home and Profile sum them. One implementation,
// so the headline total can never count a different set of assets than the
// list the user sees.
//
// Each loader returns only its own rows and touches no shared state. A failed
// read THROWS rather than answering empty, so the caller can keep that
// source's last known rows instead of showing a real balance as zero.

import AsyncStorage from '@react-native-async-storage/async-storage';
import { ethers } from 'ethers';
import {
  getWalletBalances,
  normalizeWalletBalances,
  getDiscoveredTokens,
  type DiscoveredToken,
} from './walletApi';
import { readErc20BalanceOf, formatTokenBalance } from './wallet';
import { loadTronWallet } from './chains/tronWallet';
import { loadBtcWallet } from './chains/btcWallet';
import { loadSolanaWallet, type SolanaOtherPath } from './chains/solanaWallet';
import { loadSuiWallet } from './chains/suiWallet';
import { readPremiumMcgpAsset } from './mcgpSwapApi';
import type { CustomToken } from '../hooks/useTokens';
import {
  CHAINS, getChainDisplay, isNonEVMChain,
  type ChainKey, type AnyChainKey, type TokenConfig,
} from '../constants/chains';

// ── Types ──
export interface WalletAsset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  usdPrice: number;
  iconColor: string;
  iconUrl?: string;
  /** Set on rows surfaced by the indexer instead of supported_tokens. */
  discovered?: boolean;
  /** Short label chip (e.g. "Spendable" for premium MCGP). */
  tag?: string;
  /** ERC-20 contract address; required for discovered rows so Send works. */
  contractAddress?: string;
  /** Token decimals; required for discovered rows so amount→wei conversion is correct. */
  decimals?: number;
  details?: {
    type: string;
    chain: string;
    chainKey: AnyChainKey;
  };
}

// hashColor produces a stable HSL color from a string — used as a deterministic
// avatar fallback for discovered tokens we don't have an icon for.
export function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 55%, 50%)`;
}

// mergeDiscoveredTokens fetches the auto-discovery list and returns the rows
// that aren't already in `existing` (matched by chainKey+symbol or
// chainKey+contract). Returns ONLY the additions, never the combined list: the
// caller holds the per-source rows separately and a stale combined list would
// shadow balances that had since been refreshed. Empty when there is nothing to
// add; null when the indexer could not be read, so the caller can keep what it
// already shows instead of dropping it.
export async function mergeDiscoveredTokens(
  existing: WalletAsset[],
  address: string | undefined,
): Promise<WalletAsset[] | null> {
  try {
    const result = await getDiscoveredTokens(undefined, address);
    if (!result.success || !result.data || !result.data.providerAvailable) {
      return null;
    }
    const discovered: DiscoveredToken[] = result.data.tokens || [];
    if (discovered.length === 0) return [];

    const listed = listedIn(existing);
    let nextId = existing.length + 1;
    const additions: WalletAsset[] = [];
    for (const t of discovered) {
      const chainKey = t.chain as ChainKey;
      const chain = CHAINS[chainKey];
      if (!chain) continue;
      if (listed(chainKey, t.symbol, t.contractAddress)) continue;

      const balance = formatTokenBalance(t.balance, t.decimals);
      const usdValue = Number.isFinite(t.usdValue) ? t.usdValue : 0;
      additions.push({
        id: String(nextId++),
        symbol: t.symbol,
        name: t.name || t.symbol,
        balance,
        usdValue,
        usdPrice: balance > 0 && usdValue > 0 ? usdValue / balance : 0,
        iconColor: hashColor(t.symbol),
        iconUrl: t.logoUrl,
        discovered: true,
        contractAddress: t.contractAddress,
        decimals: t.decimals,
        details: { type: 'Discovered', chain: chain.name, chainKey },
      });
    }
    return additions;
  } catch (err) {
    console.warn('[wallet] discovery merge failed:', err);
    return null;
  }
}

// Whether a token is already among `existing`, by chainKey+symbol OR
// chainKey+contract — so neither USDC-on-base (matched by symbol) nor a custom
// token previously imported (matched by address) is listed twice.
export function listedIn(existing: WalletAsset[]) {
  const seenSymbol = new Set(
    existing
      .filter((a) => a.details?.chainKey)
      .map((a) => `${a.details!.chainKey}::${a.symbol.toUpperCase()}`),
  );
  const seenContract = new Set(
    existing
      .filter((a) => a.contractAddress && a.details?.chainKey)
      .map((a) => `${a.details!.chainKey}::${a.contractAddress!.toLowerCase()}`),
  );
  return (chainKey: string, symbol: string, contractAddress?: string) =>
    seenSymbol.has(`${chainKey}::${symbol.toUpperCase()}`) ||
    (!!contractAddress && seenContract.has(`${chainKey}::${contractAddress.toLowerCase()}`));
}

// mergeCustomTokens surfaces user-imported tokens in the asset list and tries
// to attach an on-chain balance for each. We always add the token (with
// balance 0 if the read fails) so the user can see what they imported — public
// RPCs go down often enough that silently dropping the row makes imports look
// "lost".
export async function mergeCustomTokens(
  existing: WalletAsset[],
  address: string | undefined,
  customTokens: CustomToken[],
): Promise<WalletAsset[]> {
  if (customTokens.length === 0) return [];
  try {
    const seenContract = new Set(
      existing
        .filter((a) => a.contractAddress && a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.contractAddress!.toLowerCase()}`),
    );

    const balances = address
      ? await Promise.all(
          customTokens.map(async (t) => {
            // Non-EVM custom tokens (e.g. imported SPL mints) have no ERC-20
            // balanceOf; their balance is already reflected by the nonevm
            // loaders' auto-discovery and dedups against `existing` below.
            if (isNonEVMChain(t.chainKey)) return null;
            try {
              const raw = await readErc20BalanceOf(t.contractAddress, address, t.chainKey);
              return Number(ethers.formatUnits(raw, t.decimals));
            } catch (err) {
              console.warn(
                `[wallet] balanceOf failed for custom ${t.symbol} on ${t.chainKey}:`,
                err,
              );
              return null;
            }
          }),
        )
      : customTokens.map(() => null);

    let nextId = existing.length + 1;
    const additions: WalletAsset[] = [];

    customTokens.forEach((token, i) => {
      const contractKey = `${token.chainKey}::${token.contractAddress.toLowerCase()}`;
      if (seenContract.has(contractKey)) return;
      // getChainDisplay resolves both EVM and non-EVM chains (CHAINS is EVM-only).
      const chain = getChainDisplay(token.chainKey);
      if (!chain) return;

      const balance = balances[i] ?? 0;

      additions.push({
        id: String(nextId++),
        symbol: token.symbol,
        name: token.name,
        balance,
        usdValue: 0,
        usdPrice: 0,
        iconColor: token.iconColor || hashColor(token.symbol),
        iconUrl: token.iconUrl,
        contractAddress: token.contractAddress,
        decimals: token.decimals,
        details: { type: 'Imported', chain: chain.name, chainKey: token.chainKey },
      });
    });

    return additions;
  } catch (err) {
    console.warn('[wallet] custom-token merge failed:', err);
    return [];
  }
}

// Only native tokens form the skeleton — ERC-20s are added dynamically
// from balance responses and discovery (balance > 0 only).
export function buildNativeAssets(): WalletAsset[] {
  return Object.entries(CHAINS).map(([chainKey, chain], i) => ({
    id: String(i + 1),
    symbol: chain.nativeCurrency.symbol,
    name: chain.name,
    balance: 0, usdValue: 0, usdPrice: 0,
    iconColor: chain.iconColor,
    iconUrl: chain.iconUrl,
    details: { type: 'Native Token', chain: chain.name, chainKey: chainKey as ChainKey },
  }));
}

// ── Per-source loaders ────────────────────────────────────────────────────
// Each returns only its own assets and touches no shared state, which is what
// lets them run concurrently. A failed read THROWS rather than answering
// empty: the orchestrator then keeps that source's last known rows, so an
// unreachable chain never shows (or saves) a real balance as zero.

export async function loadEvmAssets(tokenList: TokenConfig[], address?: string): Promise<WalletAsset[]> {
  const nativeAssets = buildNativeAssets();
  const result = await getWalletBalances(undefined, address);
  if (!result.success || !result.data) throw new Error(result.message || 'EVM balances unavailable');
  const normalized = normalizeWalletBalances(result.data);
  const balanceMap = new Map(normalized.map(e => [`${e.symbol}-${e.chainKey}`, e] as const));

  // Overlay balances onto native tokens
  let id = nativeAssets.length + 1;
  const updated: WalletAsset[] = nativeAssets.map(asset => {
    const entry = balanceMap.get(`${asset.symbol}-${asset.details!.chainKey}`);
    if (!entry) return asset;
    const balance = parseFloat(entry.balance) || 0;
    const usdValue = parseFloat(entry.usdValue) || 0;
    return { ...asset, balance, usdValue, usdPrice: entry.usdPrice || (balance > 0 ? usdValue / balance : 0) };
  });

  // Add ERC-20s only if balance > 0
  for (const entry of normalized) {
    const bal = parseFloat(entry.balance);
    if (!bal || bal <= 0) continue;
    const isNative = nativeAssets.some(
      a => a.symbol === entry.symbol && a.details?.chainKey === entry.chainKey
    );
    if (isNative) continue;

    // EVM-only legacy view: balances come from the EVM fan-out, so
    // chainKey is always an EVM ChainKey here.
    const evmChainKey = entry.chainKey as ChainKey;
    const tokenConfig = tokenList.find(
      t => t.symbol === entry.symbol && t.chains.includes(evmChainKey)
    );
    const chain = CHAINS[evmChainKey];
    if (!chain) continue;

    updated.push({
      id: String(id++),
      symbol: entry.symbol,
      name: tokenConfig?.name || entry.name || entry.symbol,
      balance: bal,
      usdValue: parseFloat(entry.usdValue) || 0,
      usdPrice: entry.usdPrice || 0,
      iconColor: tokenConfig?.iconColor || '#888',
      iconUrl: tokenConfig?.iconUrl,
      contractAddress: entry.contractAddress || undefined,
      decimals: entry.decimals,
      details: {
        type: entry.symbol === 'MCGP' ? 'Gold-Backed Token' : 'Token',
        chain: chain.name,
        chainKey: evmChainKey,
      },
    });
  }
  return updated;
}

export async function loadTronAssets(addr: string, tokenList: TokenConfig[]): Promise<WalletAsset[]> {
  // Non-EVM (Tron): derive the address from the same seed, self-register,
  // and append TRX + TRC-20 balances as assets. Dormant-safe — yields
  // nothing when the backend chain is unconfigured. TRX shows as a native
  // token (visible at 0 in the zero-balance section) so Tron always appears.
  const tron = await loadTronWallet(addr || undefined);
  if (tron?.failed) throw new Error('Tron balances unavailable');
  if (!tron) return [];
  const tronChain = getChainDisplay('tron');
  return tron.balances.map((row, i) => {
    const isNative = row.symbol === 'TRX';
    return {
      id: `tron-${i + 1}`,
      symbol: row.symbol,
      name: isNative ? 'Tron' : row.name,
      balance: parseFloat(row.balance) || 0,
      usdValue: parseFloat(row.usdValue) || 0,
      usdPrice: row.usdPrice || 0,
      iconColor: tronChain?.iconColor || '#EF0027',
      // TRX gets the chain logo; TRC-20s (USDT) reuse the registry logo.
      iconUrl: isNative ? tronChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
      contractAddress: row.contractAddress || undefined,
      decimals: row.decimals,
      details: {
        type: isNative ? 'Native Token' : 'Token',
        chain: 'Tron',
        chainKey: 'tron',
      },
    };
  });
}

export async function loadBtcAssets(addr: string): Promise<WalletAsset[]> {
  // Non-EVM (BTC): same pattern — the BIP-84 address derives from the same
  // seed, self-registers, and BTC shows as a native token (visible at 0 in
  // the zero-balance section) so Bitcoin always appears.
  const btc = await loadBtcWallet(addr || undefined);
  if (btc?.failed) throw new Error('BTC balances unavailable');
  if (!btc) return [];
  const btcChain = getChainDisplay('btc');
  return btc.balances.map((row, i) => ({
    id: `btc-${i + 1}`,
    symbol: row.symbol,
    name: row.symbol === 'BTC' ? 'Bitcoin' : row.name,
    balance: parseFloat(row.balance) || 0,
    usdValue: parseFloat(row.usdValue) || 0,
    usdPrice: row.usdPrice || 0,
    iconColor: btcChain?.iconColor || '#F7931A',
    iconUrl: btcChain?.iconUrl,
    decimals: row.decimals,
    details: { type: 'Native Token', chain: 'Bitcoin', chainKey: 'btc' },
  }));
}

export async function loadSolanaAssets(
addr: string,
tokenList: TokenConfig[],
onOtherPath?: (path: SolanaOtherPath | null) => void,
): Promise<WalletAsset[]> {
  // Non-EVM (Solana): the address derives from the same seed (Phantom or
  // Trust Wallet path, whichever holds the funds), self-registers, and SOL
  // shows as a native token (visible at 0 in the zero-balance section) so
  // Solana always appears; held SPL tokens (auto-discovered) ride alongside.
  onOtherPath?.(null);
  const sol = await loadSolanaWallet(addr || undefined);
  if (sol?.failed) throw new Error('Solana balances unavailable');
  onOtherPath?.(sol?.otherPath ?? null);
  if (!sol) return [];
  const solChain = getChainDisplay('solana');
  return sol.balances.map((row, i) => {
    const isNative = row.symbol === 'SOL';
    return {
      id: `solana-${i + 1}`,
      symbol: row.symbol,
      name: isNative ? 'Solana' : row.name,
      balance: parseFloat(row.balance) || 0,
      usdValue: parseFloat(row.usdValue) || 0,
      usdPrice: row.usdPrice || 0,
      iconColor: solChain?.iconColor || '#14F195',
      iconUrl: isNative ? solChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
      contractAddress: row.contractAddress || undefined,
      decimals: row.decimals,
      details: {
        type: isNative ? 'Native Token' : 'Token',
        chain: 'Solana',
        chainKey: 'solana',
      },
    };
  });
}

export async function loadSuiAssets(addr: string, tokenList: TokenConfig[]): Promise<WalletAsset[]> {
  // Non-EVM (Sui): same seed, self-registering, and SUI shows as a native
  // token (visible at 0 in the zero-balance section) so Sui always appears;
  // held coin types ride alongside.
  const sui = await loadSuiWallet(addr || undefined);
  if (sui?.failed) throw new Error('Sui balances unavailable');
  if (!sui) return [];
  const suiChain = getChainDisplay('sui');
  return sui.balances.map((row, i) => {
    const isNative = row.symbol === 'SUI';
    return {
      id: `sui-${i + 1}`,
      symbol: row.symbol,
      name: isNative ? 'Sui' : row.name,
      balance: parseFloat(row.balance) || 0,
      usdValue: parseFloat(row.usdValue) || 0,
      usdPrice: row.usdPrice || 0,
      iconColor: suiChain?.iconColor || '#4DA2FF',
      iconUrl: isNative ? suiChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
      contractAddress: row.contractAddress || undefined,
      decimals: row.decimals,
      details: {
        type: isNative ? 'Native Token' : 'Token',
        chain: 'Sui',
        chainKey: 'sui',
      },
    };
  });
}

export async function loadPremiumAssets(tokenList: TokenConfig[]): Promise<WalletAsset[]> {
  // Spendable premium MCGP lives in Contract B (not as ERC-20 in the
  // wallet), so the standard balance read misses it. Show it as its OWN
  // tagged row — distinct from any ordinary (withdrawn) ERC-20 MCGP — so
  // the user can tell spendable from transferable.
  const { balance: premium, usdValue } = await readPremiumMcgpAsset();
  if (premium <= 0) return [];
  const mcgpToken = tokenList.find(t => t.symbol === 'MCGP');
  return [{
    id: 'mcgp-premium',
    symbol: 'MCGP',
    name: mcgpToken?.name || 'MCG Protocol',
    balance: premium,
    usdValue,
    usdPrice: usdValue / premium,
    iconColor: mcgpToken?.iconColor || '#FFD700',
    iconUrl: mcgpToken?.iconUrl,
    tag: 'Spendable',
    details: { type: 'Token', chain: 'Sonic', chainKey: 'sonic' },
  }];
}

// ── Hidden tokens ──
// The Wallet tab's "Manage tokens" list. A hidden token is left out of the
// total everywhere, so Home never counts what the Wallet tab doesn't.

export const HIDDEN_TOKENS_KEY = 'wallet_hidden_tokens';

export const tokenKey = (chainKey: string, symbol: string, contractAddress?: string) =>
  `${chainKey}::${contractAddress || symbol}`;

export async function readHiddenTokens(): Promise<Set<string>> {
  try {
    const raw = await AsyncStorage.getItem(HIDDEN_TOKENS_KEY);
    return new Set(raw ? JSON.parse(raw) : []);
  } catch {
    return new Set();
  }
}

export function isHidden(asset: WalletAsset, hidden: Set<string>): boolean {
  return hidden.has(tokenKey(asset.details?.chainKey || '', asset.symbol, asset.contractAddress));
}

/** USD total of the rows the user can see: hidden tokens excluded. */
export function visibleUsdTotal(assets: WalletAsset[], hidden: Set<string>): number {
  return assets.reduce(
    (s, a) => (isHidden(a, hidden) || !Number.isFinite(a.usdValue) ? s : s + a.usdValue),
    0,
  );
}
