// services/wallet.ts
// Core crypto wallet service for TSA Connect — multi-wallet support
import 'react-native-get-random-values';
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
 * Sign a transaction with the stored private key
 */
export async function signTransaction(unsignedTx: ethers.TransactionLike): Promise<string> {
  const privateKey = await getPrivateKey();
  if (!privateKey) {
    throw new Error('No private key found. Please set up your wallet first.');
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
 * Read ERC20 token metadata from a contract address.
 * Returns null if the contract doesn't implement standard ERC20 metadata.
 */
export async function readTokenContract(
  contractAddress: string,
  chainKey: ChainKey
): Promise<{ name: string; symbol: string; decimals: number } | null> {
  try {
    const provider = getProvider(chainKey);
    const abi = [
      'function name() view returns (string)',
      'function symbol() view returns (string)',
      'function decimals() view returns (uint8)',
    ];
    const contract = new ethers.Contract(contractAddress, abi, provider);
    const [name, symbol, decimals] = await Promise.all([
      contract.name(),
      contract.symbol(),
      contract.decimals(),
    ]);
    return { name, symbol, decimals: Number(decimals) };
  } catch (err) {
    console.error('readTokenContract error:', err);
    return null;
  }
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
 */
export function getProvider(chainKey: ChainKey): ethers.JsonRpcProvider {
  const cached = providerCache.get(chainKey);
  if (cached) return cached;

  const chain: ChainConfig = CHAINS[chainKey];
  const provider = new ethers.JsonRpcProvider(chain.rpcUrl, {
    chainId: chain.chainId,
    name: chain.name,
  });

  providerCache.set(chainKey, provider);
  return provider;
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
