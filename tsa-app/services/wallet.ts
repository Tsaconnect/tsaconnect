// services/wallet.ts
// Core crypto wallet service for TSA Connect
import 'react-native-get-random-values';
import { ethers } from 'ethers';
import * as SecureStore from 'expo-secure-store';
import { CHAINS, type ChainKey, type ChainConfig } from '../constants/chains';

const WALLET_KEY = 'tsa-wallet-key';
const MNEMONIC_KEY = 'tsa-wallet-mnemonic';

// Provider cache — one instance per chain
const providerCache = new Map<ChainKey, ethers.JsonRpcProvider>();

export interface WalletInfo {
  mnemonic: string;
  address: string;
  privateKey: string;
}

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
 * Store private key in secure storage
 */
export async function storePrivateKey(privateKey: string): Promise<void> {
  await SecureStore.setItemAsync(WALLET_KEY, privateKey);
}

/**
 * Store mnemonic phrase in secure storage (separate from private key)
 */
export async function storeMnemonic(mnemonic: string): Promise<void> {
  await SecureStore.setItemAsync(MNEMONIC_KEY, mnemonic);
}

/**
 * Retrieve private key from secure storage
 */
export async function getPrivateKey(): Promise<string | null> {
  try {
    return await SecureStore.getItemAsync(WALLET_KEY);
  } catch (error) {
    console.error('Failed to retrieve private key:', error);
    return null;
  }
}

/**
 * Retrieve stored mnemonic phrase from secure storage
 */
export async function getMnemonic(): Promise<string | null> {
  try {
    return await SecureStore.getItemAsync(MNEMONIC_KEY);
  } catch (error) {
    console.error('Failed to retrieve mnemonic:', error);
    return null;
  }
}

/**
 * Delete private key and mnemonic from secure storage
 */
export async function deletePrivateKey(): Promise<void> {
  await SecureStore.deleteItemAsync(WALLET_KEY);
  await SecureStore.deleteItemAsync(MNEMONIC_KEY);
}

/**
 * Sign a transaction with the stored private key
 */
export async function signTransaction(unsignedTx: ethers.TransactionLike): Promise<string> {
  const privateKey = await getPrivateKey();
  if (!privateKey) {
    throw new Error('No private key found. Please set up your wallet first.');
  }

  const wallet = new ethers.Wallet(privateKey);
  const signedTx = await wallet.signTransaction(unsignedTx);
  return signedTx;
}

/**
 * Import a wallet from an existing mnemonic (seed phrase)
 */
export async function importWalletFromMnemonic(mnemonic: string): Promise<{ address: string }> {
  const trimmed = mnemonic.trim().toLowerCase();

  if (!ethers.Mnemonic.isValidMnemonic(trimmed)) {
    throw new Error('Invalid mnemonic phrase. Please check your words and try again.');
  }

  const wallet = ethers.HDNodeWallet.fromPhrase(trimmed);

  await storePrivateKey(wallet.privateKey);
  await storeMnemonic(trimmed);

  return { address: wallet.address };
}

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
