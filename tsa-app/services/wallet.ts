// services/wallet.ts
// Core crypto wallet service for TSA Connect — multi-wallet support
import 'react-native-get-random-values';
import { Alert } from 'react-native';
import { ethers } from 'ethers';
import * as SecureStore from 'expo-secure-store';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { CHAINS, type ChainKey, type ChainConfig } from '../constants/chains';

const WALLET_LIST_KEY = 'tsa-wallet-list';
const ACTIVE_WALLET_KEY = 'tsa-active-wallet';
const LEGACY_WALLET_KEY = 'tsa-wallet-key';
const LEGACY_MNEMONIC_KEY = 'tsa-wallet-mnemonic';

// Provider cache — one instance per chain
const providerCache = new Map<ChainKey, ethers.JsonRpcProvider>();

export interface WalletMeta {
  address: string;
  label: string;
  backedUp: boolean;
  createdAt: string;
}

export interface WalletInfo {
  mnemonic: string;
  address: string;
  privateKey: string;
}

// ---------------------------------------------------------------------------
// Wallet list & active wallet
// ---------------------------------------------------------------------------

export async function getWalletList(): Promise<WalletMeta[]> {
  const json = await AsyncStorage.getItem(WALLET_LIST_KEY);
  if (!json) return [];
  return JSON.parse(json);
}

export async function getActiveWallet(): Promise<string | null> {
  const active = await AsyncStorage.getItem(ACTIVE_WALLET_KEY);
  if (active) return active;

  // Fallback: legacy key from before multi-wallet migration
  const legacy = await AsyncStorage.getItem('walletAddress');
  if (legacy) {
    // Sync to new key so future reads are fast
    await AsyncStorage.setItem(ACTIVE_WALLET_KEY, legacy);
  }
  return legacy;
}

export async function setActiveWallet(address: string): Promise<void> {
  await AsyncStorage.setItem(ACTIVE_WALLET_KEY, address);
  await AsyncStorage.setItem('walletAddress', address);
}

/**
 * The active wallet, but only when this device actually holds its signing key.
 *
 * `getActiveWallet` reads AsyncStorage, which survives a reinstall-and-restore,
 * a logout, and a wallet the user removed — none of which leave the SecureStore
 * key behind. So an address alone proves nothing about whether we can spend
 * from it, and treating it as proof is what made a fresh device render MCGP
 * the user had no way to touch, as if we were holding it for them.
 *
 * Use this anywhere a balance is about to be SHOWN or an action offered. Use
 * `getActiveWallet` only where a bare address is genuinely enough (a receive
 * QR, a label).
 */
export async function getSignableWallet(): Promise<string | null> {
  const address = await getActiveWallet();
  if (!address) return null;
  const privateKey = await getPrivateKey(address);
  return privateKey ? address : null;
}

// ---------------------------------------------------------------------------
// Add / remove / update wallets
// ---------------------------------------------------------------------------

export async function addWallet(info: WalletInfo, label: string): Promise<void> {
  const list = await getWalletList();
  if (list.some(w => w.address.toLowerCase() === info.address.toLowerCase())) {
    throw new Error('This wallet is already added');
  }
  await SecureStore.setItemAsync(`tsa-wallet-pk-${info.address}`, info.privateKey);
  await SecureStore.setItemAsync(`tsa-wallet-mn-${info.address}`, info.mnemonic);
  list.push({
    address: info.address,
    label,
    backedUp: false,
    createdAt: new Date().toISOString(),
  });
  await AsyncStorage.setItem(WALLET_LIST_KEY, JSON.stringify(list));
  await setActiveWallet(info.address);
}

export async function removeWallet(address: string): Promise<void> {
  await SecureStore.deleteItemAsync(`tsa-wallet-pk-${address}`);
  await SecureStore.deleteItemAsync(`tsa-wallet-mn-${address}`);
  let list = await getWalletList();
  list = list.filter(w => w.address !== address);
  await AsyncStorage.setItem(WALLET_LIST_KEY, JSON.stringify(list));
  const active = await getActiveWallet();
  if (active === address) {
    if (list.length > 0) {
      await setActiveWallet(list[0].address);
    } else {
      await AsyncStorage.removeItem(ACTIVE_WALLET_KEY);
      await AsyncStorage.removeItem('walletAddress');
    }
  }
}

export async function updateWalletLabel(address: string, label: string): Promise<void> {
  const list = await getWalletList();
  const wallet = list.find(w => w.address === address);
  if (wallet) {
    wallet.label = label;
    await AsyncStorage.setItem(WALLET_LIST_KEY, JSON.stringify(list));
  }
}

export async function markWalletBackedUp(address: string): Promise<void> {
  const list = await getWalletList();
  const wallet = list.find(w => w.address === address);
  if (wallet) {
    wallet.backedUp = true;
    await AsyncStorage.setItem(WALLET_LIST_KEY, JSON.stringify(list));
  }
}

// ---------------------------------------------------------------------------
// Wallet integrity
// ---------------------------------------------------------------------------

export type WalletIntegrityResult =
  | { ok: true }
  | { ok: false; reason: 'missing' }
  | { ok: false; reason: 'mismatch'; derived: string };

/**
 * Check that the private key stored under `address` actually derives to that
 * address. Used as a pre-flight before flows that ask the wallet to sign on
 * behalf of `address` — running this *before* creating server-side state
 * (e.g. a pending P2P ad row) means the user sees actionable guidance instead
 * of a cryptic server-side "signature recovered to X, expected Y" error after
 * the row is already in the DB.
 *
 * Corruption modes this catches:
 *   - SecureStore key missing entirely (deleted, never written)
 *   - Stored key derives to a different address (legacy migration crossed
 *     wires, or a re-import wrote under the wrong slot)
 *
 * The same defensive check also lives inside `signTypedData` /
 * `signTransaction`, but those run *after* the API call that submits the ad.
 * Keeping a parallel check at the entry point lets the caller short-circuit
 * with the same error message before any network I/O.
 */
export async function verifyWalletKey(address: string): Promise<WalletIntegrityResult> {
  const privateKey = await getPrivateKey(address);
  if (!privateKey) {
    return { ok: false, reason: 'missing' };
  }
  const derived = ethers.computeAddress(privateKey);
  if (derived.toLowerCase() !== address.toLowerCase()) {
    return { ok: false, reason: 'mismatch', derived };
  }
  return { ok: true };
}

// ---------------------------------------------------------------------------
// Wallet generation & import
// ---------------------------------------------------------------------------

/**
 * Generate a new HD wallet using ethers.js
 * Creates a random 12-word BIP-39 mnemonic and derives private key + address.
 * The same address works on all EVM chains.
 */
export async function generateWallet(): Promise<WalletInfo> {
  const wallet = ethers.Wallet.createRandom();
  const mnemonic = wallet.mnemonic?.phrase;

  if (!mnemonic) {
    throw new Error('Failed to generate mnemonic');
  }

  return {
    mnemonic,
    address: wallet.address,
    privateKey: wallet.privateKey,
  };
}

/**
 * Import a wallet from an existing mnemonic (seed phrase).
 * Derives and returns wallet info without storing — caller should use addWallet().
 */
export async function importWalletFromMnemonic(mnemonic: string): Promise<WalletInfo> {
  const trimmed = mnemonic.trim().toLowerCase();

  if (!ethers.Mnemonic.isValidMnemonic(trimmed)) {
    throw new Error('Invalid mnemonic phrase. Please check your words and try again.');
  }

  const wallet = ethers.HDNodeWallet.fromPhrase(trimmed);

  return {
    mnemonic: trimmed,
    address: wallet.address,
    privateKey: wallet.privateKey,
  };
}

// ---------------------------------------------------------------------------
// Secure storage access (multi-wallet aware)
// ---------------------------------------------------------------------------

/**
 * Retrieve private key from secure storage.
 * Defaults to the active wallet when no address is provided.
 */
export async function getPrivateKey(address?: string): Promise<string | null> {
  try {
    const addr = address || await getActiveWallet();
    if (!addr) return null;
    return await SecureStore.getItemAsync(`tsa-wallet-pk-${addr}`);
  } catch (error) {
    console.error('Failed to retrieve private key:', error);
    return null;
  }
}

/**
 * Retrieve stored mnemonic phrase from secure storage.
 * Defaults to the active wallet when no address is provided.
 */
export async function getMnemonic(address?: string): Promise<string | null> {
  try {
    const addr = address || await getActiveWallet();
    if (!addr) return null;
    return await SecureStore.getItemAsync(`tsa-wallet-mn-${addr}`);
  } catch (error) {
    console.error('Failed to retrieve mnemonic:', error);
    return null;
  }
}

// ---------------------------------------------------------------------------
// Transaction signing
// ---------------------------------------------------------------------------

/**
 * Sign an EIP-712 typed-data payload with the stored private key.
 *
 * Used by the P2P pool/voucher flow: the backend returns a `VoucherTypedData`
 * payload at ad-create time, the wallet signs it off-chain (no gas, no
 * broadcast), and the signature is submitted back via `adsApi.sign`.
 *
 * `domain`, `types`, and `message` accept the same shapes as ethers'
 * `Wallet.signTypedData`. The EIP712Domain entry must be stripped from
 * `types` before passing to ethers — its `_TypedDataEncoder.from()`
 * throws with "ambiguous primary types or unused types" when both the
 * domain entry and the primary type are present. The backend's JSON wire
 * payload includes EIP712Domain (per the EIP-712 spec) so we accept it
 * and remove it here, letting callers forward the server payload as-is.
 */
export async function signTypedData(
  domain: ethers.TypedDataDomain,
  types: Record<string, ethers.TypedDataField[]>,
  message: Record<string, any>,
  /**
   * Wallet address to sign with. Pass the same address the caller used
   * to populate the payload (e.g. the ad's `sellerAddress`) so the
   * signature is guaranteed to recover to that wallet. Omit only when
   * you're sure no other code path can flip the active wallet between
   * the caller's read and this call.
   */
  expectedAddress?: string,
): Promise<string> {
  const addr = expectedAddress || (await getActiveWallet()) || undefined;
  const privateKey = await getPrivateKey(addr);
  if (!privateKey) {
    throw new Error(
      addr
        ? `No private key stored for wallet ${addr}.`
        : 'No private key found. Please set up your wallet first.',
    );
  }
  const wallet = new ethers.Wallet(privateKey);
  // Defensive: SecureStore can hold a mismatched (address, key) pair if a
  // legacy migration or stale storage corrupted the wallet list. Catch it
  // here instead of letting the backend reject the signature with the
  // generic "recovered != expected" error, which doesn't hint at the
  // local-storage corruption.
  if (addr && wallet.address.toLowerCase() !== addr.toLowerCase()) {
    throw new Error(
      `Wallet key mismatch — stored key for ${addr} derives to ${wallet.address}. ` +
        `The wallet list is corrupt; remove and re-import this wallet from its seed phrase.`,
    );
  }
  const cleaned: Record<string, ethers.TypedDataField[]> = {};
  for (const k of Object.keys(types)) {
    if (k !== 'EIP712Domain') cleaned[k] = types[k];
  }
  return wallet.signTypedData(domain, cleaned, message);
}

/**
 * Sign a transaction with the stored private key.
 *
 * Pass `expectedAddress` to pin signing to a specific wallet (e.g. the
 * one whose `from` is encoded in the unsigned tx). Without it we sign
 * with whatever the active wallet happens to be at call time, which
 * can race against a user switching wallets mid-flow.
 */
export async function signTransaction(
  unsignedTx: ethers.TransactionLike,
  expectedAddress?: string,
): Promise<string> {
  const addr = expectedAddress || (await getActiveWallet()) || undefined;
  const privateKey = await getPrivateKey(addr);
  if (!privateKey) {
    throw new Error(
      addr
        ? `No private key stored for wallet ${addr}.`
        : 'No private key found. Please set up your wallet first.',
    );
  }
  // Defensive: detect (address, key) mismatch in storage before the chain
  // rejects the tx with a generic signature error.
  if (addr) {
    const derived = ethers.computeAddress(privateKey);
    if (derived.toLowerCase() !== addr.toLowerCase()) {
      throw new Error(
        `Wallet key mismatch — stored key for ${addr} derives to ${derived}. ` +
          `Remove and re-import this wallet from its seed phrase.`,
      );
    }
  }

  // Build a Transaction object and sign with SigningKey for proper EIP-155
  const tx = ethers.Transaction.from({
    type: 0, // legacy
    to: unsignedTx.to,
    value: unsignedTx.value || '0x0',
    data: unsignedTx.data || '0x',
    nonce: Number(unsignedTx.nonce || 0),
    gasLimit: unsignedTx.gasLimit,
    gasPrice: unsignedTx.gasPrice,
    chainId: Number(unsignedTx.chainId || 146),
  });

  const signingKey = new ethers.SigningKey(privateKey);
  tx.signature = signingKey.sign(tx.unsignedHash);

  return tx.serialized;
}

/**
 * Convert a raw integer balance string (smallest-unit, e.g. wei) to a
 * human-readable number using the token's decimals. Uses string slicing
 * instead of Number()/division to avoid IEEE-754 precision loss on large
 * 18-decimal balances. Returns 0 for empty/zero inputs.
 *
 * Shared by every place that has to render an indexer's raw `balance` field
 * (wallet home, All Tokens screen, etc.) — keeping it in one spot prevents
 * the "tokens.tsx forgot to divide by decimals" class of bug.
 */
export function formatTokenBalance(rawBalance: string, decimals: number): number {
  if (!rawBalance || rawBalance === '0') return 0;
  if (decimals <= 0) return parseFloat(rawBalance);
  const padded = rawBalance.padStart(decimals + 1, '0');
  const cut = padded.length - decimals;
  const whole = padded.slice(0, cut) || '0';
  const frac = padded.slice(cut).replace(/0+$/, '');
  return parseFloat(frac.length > 0 ? `${whole}.${frac}` : whole);
}

/**
 * Read the native gas-token balance (in wei) for an address on the given
 * chain. Uses the same RPC fallback strategy as other read helpers.
 *
 * Returns null when every configured RPC fails — callers should treat null
 * as "balance unknown" and degrade gracefully (do NOT hard-block the user).
 */
export async function nativeBalance(chainKey: ChainKey, address: string): Promise<bigint | null> {
  try {
    return await withRpcFallback(chainKey, (provider) => provider.getBalance(address));
  } catch (err) {
    console.warn('[wallet] nativeBalance read failed, degrading gracefully:', err);
    return null;
  }
}

/**
 * Gas guard: check that `address` holds at least some native gas token on
 * `chainKey` before broadcasting. Calls `onAlert` with a human-readable
 * message if the balance is zero (or near-zero). Returns `true` when the
 * user can proceed, `false` when they should fund first.
 *
 * Degrades gracefully on RPC failure — if the balance read returns null,
 * this function returns `true` (proceed) and logs a warning rather than
 * blocking the user on a network blip.
 *
 * @param chainKey  Chain to check (must match the tx's target chain).
 * @param address   Wallet address that will pay gas.
 * @param onAlert   Called with (title, message) when gas is insufficient.
 *                  Default: Alert.alert.
 */
export async function ensureGas(
  chainKey: ChainKey,
  address: string,
  onAlert?: (title: string, message: string) => void,
): Promise<boolean> {
  const balance = await nativeBalance(chainKey, address);
  if (balance === null) {
    // RPC blip — can't determine gas; let the broadcast attempt proceed.
    // The chain will reject it with "insufficient funds" if truly empty,
    // which is no worse than the pre-guard behaviour.
    return true;
  }
  if (balance === 0n) {
    const chain = CHAINS[chainKey];
    const symbol = chain.nativeCurrency.symbol;
    const name = chain.name;
    const title = 'Not enough gas';
    const message = `You need ${symbol} on ${name} to cover the transaction fee. Fund your wallet and try again.`;
    if (onAlert) {
      onAlert(title, message);
    } else {
      Alert.alert(title, message);
    }
    return false;
  }
  return true;
}

/**
 * Run a read against a chain's RPC, falling back through the configured
 * `fallbackRpcUrls` when the primary endpoint fails with a transport-level
 * error. Mirrors the strategy in `broadcastTransaction` but for reads.
 *
 * Public RPCs (bsc-dataseed, llamarpc, polygon-rpc, …) routinely rate-limit
 * or 502 in mobile contexts. Without this, contract reads on non-Sonic chains
 * appear "broken" to the user even though the chain is fine.
 */
export async function withRpcFallback<T>(
  chainKey: ChainKey,
  fn: (provider: ethers.JsonRpcProvider) => Promise<T>,
): Promise<T> {
  const chain = CHAINS[chainKey];
  const urls = [chain.rpcUrl, ...(chain.fallbackRpcUrls ?? [])];
  let lastErr: any;
  for (const url of urls) {
    try {
      const provider = getProvider(chainKey, url);
      return await fn(provider);
    } catch (err: any) {
      lastErr = err;
      const msg = String(err?.message ?? err);
      const code = err?.code;
      const isTransport =
        msg.includes('Network request failed') ||
        msg.includes('failed to detect network') ||
        msg.includes('SERVER_ERROR') ||
        msg.includes('could not coalesce error') ||
        msg.includes('TIMEOUT') ||
        msg.includes('timeout') ||
        msg.includes('429') ||
        msg.includes('502') ||
        msg.includes('503') ||
        msg.includes('504') ||
        code === 'NETWORK_ERROR' ||
        code === 'TIMEOUT' ||
        code === 'SERVER_ERROR';
      console.warn(`[wallet] RPC read via ${url} failed: ${msg}`);
      if (!isTransport) throw err;
    }
  }
  throw lastErr ?? new Error(`All RPCs failed for ${chainKey}`);
}

const ERC20_METADATA_ABI = [
  'function name() view returns (string)',
  'function symbol() view returns (string)',
  'function decimals() view returns (uint8)',
];

const ERC20_BALANCE_ABI = ['function balanceOf(address) view returns (uint256)'];

/**
 * Read ERC20 token metadata from a contract address.
 * Returns null if the contract doesn't implement standard ERC20 metadata.
 */
export async function readTokenContract(
  contractAddress: string,
  chainKey: ChainKey
): Promise<{ name: string; symbol: string; decimals: number } | null> {
  try {
    return await withRpcFallback(chainKey, async (provider) => {
      const contract = new ethers.Contract(contractAddress, ERC20_METADATA_ABI, provider);
      const [name, symbol, decimals] = await Promise.all([
        contract.name(),
        contract.symbol(),
        contract.decimals(),
      ]);
      return { name, symbol, decimals: Number(decimals) };
    });
  } catch (err) {
    console.error('readTokenContract error:', err);
    return null;
  }
}

/**
 * Read an ERC20 `balanceOf` against the given chain, trying fallback RPCs
 * if the primary is unreachable. Throws if every configured RPC fails so
 * the caller can distinguish "unknown balance" from "balance is zero".
 */
export async function readErc20BalanceOf(
  contractAddress: string,
  ownerAddress: string,
  chainKey: ChainKey,
): Promise<bigint> {
  return await withRpcFallback(chainKey, async (provider) => {
    const contract = new ethers.Contract(contractAddress, ERC20_BALANCE_ABI, provider);
    return (await contract.balanceOf(ownerAddress)) as bigint;
  });
}

// ---------------------------------------------------------------------------
// Validation helpers
// ---------------------------------------------------------------------------

/**
 * Validate that a string is a valid BIP-39 mnemonic
 */
export function isValidMnemonic(mnemonic: string): boolean {
  return ethers.Mnemonic.isValidMnemonic(mnemonic);
}

/**
 * Validate that a string is a valid EVM address
 */
export function isValidAddress(address: string): boolean {
  return ethers.isAddress(address);
}

// ---------------------------------------------------------------------------
// Provider
// ---------------------------------------------------------------------------

/**
 * Clear the provider cache. Call this when switching networks (mainnet/testnet)
 * so providers reconnect with the correct RPC URLs.
 */
export function clearProviderCache() {
  providerCache.clear();
}

/**
 * Get a JSON-RPC provider for a given chain.
 * Providers are cached so repeated calls return the same instance.
 *
 * `staticNetwork` tells ethers the chain identity is known and to skip the
 * background `eth_chainId` probe. Without it, a flaky mobile connection can
 * land in a "JsonRpcProvider failed to detect network" retry loop that never
 * succeeds and silently hangs every broadcast/wait call.
 */
export function getProvider(chainKey: ChainKey, rpcUrl?: string): ethers.JsonRpcProvider {
  const chain: ChainConfig = CHAINS[chainKey];
  const url = rpcUrl ?? chain.rpcUrl;
  // Per-URL cache so fallbacks each get their own provider instance.
  const cacheKey = `${chainKey}::${url}`;
  const cached = providerCache.get(cacheKey as ChainKey);
  if (cached) return cached;

  const network = ethers.Network.from({
    chainId: chain.chainId,
    name: chain.name,
  });
  const provider = new ethers.JsonRpcProvider(url, network, {
    staticNetwork: network,
  });

  providerCache.set(cacheKey as ChainKey, provider);
  return provider;
}

/**
 * Broadcast a signed transaction via the chain's primary RPC, falling back to
 * configured alternates if the primary throws a connection error. Returns
 * the first provider's TransactionResponse on success.
 *
 * Connection failures (the device cannot reach the URL — typical "Network
 * request failed" on Android) are retried; semantic failures (replay, nonce
 * too low, insufficient funds) are not — those are real problems the user
 * needs to see.
 */
export async function broadcastTransaction(
  chainKey: ChainKey,
  signedTx: string,
): Promise<ethers.TransactionResponse> {
  const chain = CHAINS[chainKey];
  const urls = [chain.rpcUrl, ...(chain.fallbackRpcUrls ?? [])];
  let lastErr: any;

  for (const url of urls) {
    try {
      console.log(`[wallet] broadcasting via ${url}`);
      const provider = getProvider(chainKey, url);
      return await provider.broadcastTransaction(signedTx);
    } catch (err: any) {
      lastErr = err;
      const msg = String(err?.message ?? err);
      // Only retry on transport errors. Semantic chain errors aren't going to
      // resolve by hitting a different node.
      const isTransport =
        msg.includes('Network request failed') ||
        msg.includes('failed to detect network') ||
        msg.includes('SERVER_ERROR') ||
        err?.code === 'NETWORK_ERROR' ||
        err?.code === 'TIMEOUT';
      console.warn(`[wallet] broadcast via ${url} failed: ${msg}`);
      if (!isTransport) throw err;
    }
  }
  throw lastErr ?? new Error('Failed to broadcast transaction on any configured RPC');
}

// ---------------------------------------------------------------------------
// LiFi transaction support
// ---------------------------------------------------------------------------

/**
 * Fetch the next usable nonce for an address on a given chain.
 * Uses 'pending' so it accounts for unconfirmed txs already in the mempool.
 */
export async function fetchPendingNonce(address: string, chainKey: ChainKey): Promise<number> {
  return await getProvider(chainKey).getTransactionCount(address, 'pending');
}

/**
 * Sign a LiFi transactionRequest.
 * Handles both legacy (type-0, gasPrice) and EIP-1559 (type-2, maxFeePerGas) transactions.
 * LiFi returns type-2 txs on Ethereum, Polygon, Arbitrum, Base, Optimism, Avalanche.
 */
export async function signLiFiTransaction(tx: {
  to: string;
  data: string;
  value: string;
  chainId: number;
  nonce: number;
  gasLimit: string;
  gasPrice?: string;
  maxFeePerGas?: string;
  maxPriorityFeePerGas?: string;
}): Promise<string> {
  const privateKey = await getPrivateKey();
  if (!privateKey) {
    throw new Error('No private key found. Please set up your wallet first.');
  }

  const base = {
    to: tx.to,
    value: tx.value || '0x0',
    data: tx.data || '0x',
    nonce: Number(tx.nonce),
    gasLimit: tx.gasLimit,
    chainId: Number(tx.chainId),
  };

  const txObj = tx.maxFeePerGas
    ? ethers.Transaction.from({
        ...base,
        type: 2,
        maxFeePerGas: tx.maxFeePerGas,
        maxPriorityFeePerGas: tx.maxPriorityFeePerGas ?? tx.maxFeePerGas,
      })
    : ethers.Transaction.from({
        ...base,
        type: 0,
        gasPrice: tx.gasPrice,
      });

  const signingKey = new ethers.SigningKey(privateKey);
  txObj.signature = signingKey.sign(txObj.unsignedHash);
  return txObj.serialized;
}

// ---------------------------------------------------------------------------
// Migration from single-wallet storage
// ---------------------------------------------------------------------------

/**
 * Migrate legacy single-wallet storage to the new multi-wallet format.
 * Safe to call multiple times — exits early if migration already happened.
 */
export async function migrateFromSingleWallet(): Promise<void> {
  const existingList = await AsyncStorage.getItem(WALLET_LIST_KEY);
  if (existingList) return;

  const address = await AsyncStorage.getItem('walletAddress');
  if (!address) return;

  try {
    const privateKey = await SecureStore.getItemAsync(LEGACY_WALLET_KEY);
    const mnemonic = await SecureStore.getItemAsync(LEGACY_MNEMONIC_KEY);
    const backedUp = (await AsyncStorage.getItem('seedPhraseBackedUp')) === 'true';

    if (!privateKey) return;

    await SecureStore.setItemAsync(`tsa-wallet-pk-${address}`, privateKey);
    if (mnemonic) {
      await SecureStore.setItemAsync(`tsa-wallet-mn-${address}`, mnemonic);
    }

    const list: WalletMeta[] = [{
      address,
      label: 'Wallet 1',
      backedUp,
      createdAt: new Date().toISOString(),
    }];
    await AsyncStorage.setItem(WALLET_LIST_KEY, JSON.stringify(list));
    await AsyncStorage.setItem(ACTIVE_WALLET_KEY, address);

    // Clean up legacy keys
    await SecureStore.deleteItemAsync(LEGACY_WALLET_KEY);
    await SecureStore.deleteItemAsync(LEGACY_MNEMONIC_KEY);
  } catch (error) {
    console.error('Migration failed:', error);
  }
}
