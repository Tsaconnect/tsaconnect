// signTransaction is where three independent unsigned-tx adapters converge —
// walletApi's normalizer, the P2P unsignedTxToTxLike, and the rewards claim
// path. It is therefore the only place a rule about what a signable transaction
// must carry holds for all of them.
jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
  setItem: jest.fn(),
}));

import { ethers } from 'ethers';
import { signTransaction } from '../wallet';
import { unsignedTxToTxLike } from '../p2p';

// A funded, throwaway key: these tests care about what is REFUSED before
// signing, so the key only has to be real enough to get past key loading.
const WALLET = ethers.Wallet.createRandom();

jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

jest.spyOn(require('../wallet'), 'getActiveWallet').mockResolvedValue(WALLET.address);

const signable = {
  to: '0xC0ffee254729296a45a3885639AC7E10F9d54979',
  value: '0x0',
  data: '0x',
  nonce: 4,
  gasLimit: 21000n,
  gasPrice: 1_000_000_000n,
  chainId: 146,
};

describe('signTransaction input guards', () => {
  it('refuses a transaction with no nonce rather than signing at slot 0', async () => {
    const { nonce, ...noNonce } = signable;

    await expect(signTransaction(noNonce as any, WALLET.address)).rejects.toThrow(/no nonce/i);
  });

  it('refuses a transaction that names no network rather than assuming Sonic', async () => {
    const { chainId, ...noChain } = signable;

    await expect(signTransaction(noChain as any, WALLET.address)).rejects.toThrow(/no network/i);
  });

  // The guard must reject ABSENT, not falsy: nonce 0 is a wallet's first ever
  // transaction, and treating it as missing would break exactly that case.
  it('accepts a genuine zero nonce as far as the guard is concerned', async () => {
    await expect(signTransaction({ ...signable, nonce: 0 } as any, WALLET.address)).rejects.not.toThrow(
      /no nonce/i,
    );
  });

  // These are the shapes that actually arrive. Go serializes an unset
  // `ChainID string` as '', not as a missing key, and transaction.ts passes the
  // order refund tx through with no normalizer in between — so '' reaching here
  // is the realistic case, not a hypothetical one. Number('') is 0.
  it.each([
    ['an empty string, as Go serializes an unset chain', ''],
    ['zero, which is not a chain', 0],
    ['NaN, from arithmetic on an absent field', NaN],
    ['a non-numeric string', 'sonic'],
  ])('refuses a chainId that is %s', async (_label, chainId) => {
    await expect(
      signTransaction({ ...signable, chainId } as any, WALLET.address),
    ).rejects.toThrow(/no network/i);
  });

  it.each([
    ['an empty string', ''],
    ['NaN, as `approveTx.nonce + 1` yields when the nonce is absent', NaN],
  ])('refuses a nonce that is %s', async (_label, nonce) => {
    await expect(
      signTransaction({ ...signable, nonce } as any, WALLET.address),
    ).rejects.toThrow(/no nonce/i);
  });

  // The P2P adapter used to drop an absent chainId silently; signTransaction is
  // what turns that into a refusal instead of a signature for the wrong chain.
  it('catches a chainless P2P transaction passed through the shared adapter', async () => {
    const txLike = unsignedTxToTxLike({
      to: signable.to,
      value: '0',
      data: '',
      nonce: 4,
      gasLimit: '21000',
      gasPrice: '1000000000',
    } as any);

    await expect(signTransaction(txLike as any, WALLET.address)).rejects.toThrow(/no network/i);
  });
});
