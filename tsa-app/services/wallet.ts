// services/wallet.ts
// Core crypto wallet service for TSA Connect
import { ethers } from 'ethers';
import * as Keychain from 'react-native-keychain';

const WALLET_SERVICE = 'tsa-wallet-key';
const MNEMONIC_SERVICE = 'tsa-wallet-mnemonic';

// Sonic testnet configuration
export const SONIC_TESTNET = {
  chainId: 14601,
  rpcUrl: 'https://rpc.testnet.soniclabs.com',
  name: 'Sonic Testnet',
};

export interface WalletInfo {
  mnemonic: string;
  address: string;
  privateKey: string;
}

/**
 * Generate a new HD wallet using ethers.js
 * Creates a random 12-word BIP-39 mnemonic and derives private key + address
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
 * Store private key in secure storage (Keychain / Keystore)
 * Uses biometric or device passcode protection
 */
export async function storePrivateKey(privateKey: string): Promise<void> {
  await Keychain.setGenericPassword('wallet', privateKey, {
    service: WALLET_SERVICE,
    accessControl: Keychain.ACCESS_CONTROL.BIOMETRY_ANY_OR_DEVICE_PASSCODE,
    accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
  });
}

/**
 * Store mnemonic phrase in secure storage (separate from private key)
 * This is stored so the user can view their backup phrase later
 */
export async function storeMnemonic(mnemonic: string): Promise<void> {
  await Keychain.setGenericPassword('mnemonic', mnemonic, {
    service: MNEMONIC_SERVICE,
    accessControl: Keychain.ACCESS_CONTROL.BIOMETRY_ANY_OR_DEVICE_PASSCODE,
    accessible: Keychain.ACCESSIBLE.WHEN_UNLOCKED_THIS_DEVICE_ONLY,
  });
}

/**
 * Retrieve private key from secure storage
 * Triggers biometric prompt for authorization
 */
export async function getPrivateKey(): Promise<string | null> {
  try {
    const credentials = await Keychain.getGenericPassword({
      service: WALLET_SERVICE,
      authenticationPrompt: {
        title: 'Authenticate to access wallet',
      },
    });

    if (credentials) {
      return credentials.password;
    }
    return null;
  } catch (error) {
    console.error('Failed to retrieve private key:', error);
    return null;
  }
}

/**
 * Retrieve stored mnemonic phrase from secure storage
 * Triggers biometric prompt for authorization
 */
export async function getMnemonic(): Promise<string | null> {
  try {
    const credentials = await Keychain.getGenericPassword({
      service: MNEMONIC_SERVICE,
      authenticationPrompt: {
        title: 'Authenticate to view seed phrase',
      },
    });

    if (credentials) {
      return credentials.password;
    }
    return null;
  } catch (error) {
    console.error('Failed to retrieve mnemonic:', error);
    return null;
  }
}

/**
 * Delete private key and mnemonic from secure storage
 */
export async function deletePrivateKey(): Promise<void> {
  await Keychain.resetGenericPassword({ service: WALLET_SERVICE });
  await Keychain.resetGenericPassword({ service: MNEMONIC_SERVICE });
}

/**
 * Sign a transaction with the stored private key
 * Triggers biometric authentication before signing
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
 * Validates the mnemonic, derives the wallet, and stores the key securely
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
 * Validate that a string is a valid Ethereum/Sonic address
 */
export function isValidAddress(address: string): boolean {
  return ethers.isAddress(address);
}

/**
 * Get a provider connected to Sonic testnet
 */
export function getSonicProvider(): ethers.JsonRpcProvider {
  return new ethers.JsonRpcProvider(SONIC_TESTNET.rpcUrl, {
    chainId: SONIC_TESTNET.chainId,
    name: SONIC_TESTNET.name,
  });
}
