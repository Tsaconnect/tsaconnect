/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
// signMessage is the wallet proof a name-change fee starts with: an EIP-191
// personal_sign of the server's challenge by the stored key of the named
// wallet — the same digest the API recovers the signer from
// (go-ethereum accounts.TextHash). A key that doesn't derive to the named
// wallet signs nothing.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
const mockKeys: Record<string, string> = {};
jest.mock('expo-secure-store', () => ({
  __esModule: true,
  getItemAsync: jest.fn(async (k: string) => mockKeys[k] ?? null),
  setItemAsync: jest.fn(),
  deleteItemAsync: jest.fn(),
}));

import { ethers } from 'ethers';
import { signMessage } from '../wallet';

const MESSAGE = 'TSA Connect name change\nSign to prove this wallet is yours. This moves no funds.\nNonce: 1790000000.abc';

it('signs the challenge (personal_sign) with the named wallet\'s own key', async () => {
  const wallet = ethers.Wallet.createRandom();
  mockKeys[`tsa-wallet-pk-${wallet.address}`] = wallet.privateKey;
  const signature = await signMessage(MESSAGE, wallet.address);
  expect(ethers.verifyMessage(MESSAGE, signature)).toBe(wallet.address);
  // The digest the server recovers from: "\x19Ethereum Signed Message:\n" + len + message.
  expect(ethers.recoverAddress(ethers.hashMessage(MESSAGE), signature)).toBe(wallet.address);
});

it('signs nothing for a wallet with no key, or a key that derives elsewhere', async () => {
  const named = ethers.Wallet.createRandom();
  await expect(signMessage(MESSAGE, named.address)).rejects.toThrow(/No private key stored/);
  mockKeys[`tsa-wallet-pk-${named.address}`] = ethers.Wallet.createRandom().privateKey;
  await expect(signMessage(MESSAGE, named.address)).rejects.toThrow(/key mismatch/);
});
