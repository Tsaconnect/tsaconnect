jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

// eslint-disable-next-line import/first -- jest.mock factories must hoist above this import.
import { ethers } from 'ethers';
// eslint-disable-next-line import/first
import {
  buildTransferAuthorization,
  getSponsoredRoutes,
  isGasSponsored,
  type SponsorshipOffer,
} from '../gasSponsorship';

const SIGNER_KEY = '0x' + '11'.repeat(32);
const SIGNER = '0x19E7E376E7C213B7E7e7e46cc70A5dD086DAff2A';
const DEPOSIT = '0x2222222222222222222222222222222222222222';
const NONCE = '0x' + 'ab'.repeat(32);
const FIXTURE_SIGNATURE =
  '0xa484f5c66a0d60049bdef52b6f804bcbcb48ddf0f86c125202d2e60cbe5806c245510cbb6624175e65fb4b6c73c72a395aa56be2bc4194468039fd8605f9e4061c';

const offer: SponsorshipOffer = {
  mode: 'authorization',
  chain: 'base',
  token: 'USDC',
  tokenAddress: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913',
  decimals: 6,
  from: SIGNER,
  to: DEPOSIT,
  value: '36421800',
  validBefore: 1790000000,
  domain: {
    name: 'USD Coin',
    version: '2',
    chainId: '8453',
    verifyingContract: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913',
  },
};
const order = {
  walletAddress: SIGNER.toLowerCase(),
  depositAddress: DEPOSIT,
  tokenAmount: '36.421800',
  chain: 'base',
  token: 'USDC',
};

describe('buildTransferAuthorization', () => {
  it('produces typed data whose signature matches the backend fixture', async () => {
    const typed = buildTransferAuthorization(offer, order, NONCE);
    const sig = await new ethers.Wallet(SIGNER_KEY).signTypedData(typed.domain, typed.types, typed.message);
    expect(sig).toBe(FIXTURE_SIGNATURE);
    expect(ethers.verifyTypedData(typed.domain, typed.types, typed.message, sig)).toBe(SIGNER);
  });

  it('refuses when the offer pays a different address than the order', () => {
    const other = { ...offer, to: '0x3333333333333333333333333333333333333333' };
    expect(() => buildTransferAuthorization(other, order, NONCE)).toThrow(/don't match/);
  });

  it('refuses when the offer amount differs from the order amount', () => {
    expect(() => buildTransferAuthorization({ ...offer, value: '99000000' }, order, NONCE)).toThrow(/amount/);
  });

  it('refuses when the signing domain is on another chain', () => {
    const other = { ...offer, domain: { ...offer.domain!, chainId: '42161' } };
    expect(() => buildTransferAuthorization(other, order, NONCE)).toThrow(/token details/);
  });

  it('refuses when the signing domain names a different token contract', () => {
    const other = {
      ...offer,
      domain: { ...offer.domain!, verifyingContract: '0x3333333333333333333333333333333333333333' },
    };
    expect(() => buildTransferAuthorization(other, order, NONCE)).toThrow(/token details/);
  });

  it('refuses when the offer decimals differ from the token', () => {
    expect(() => buildTransferAuthorization({ ...offer, decimals: 18 }, order, NONCE)).toThrow(/token details/);
  });

  it('refuses a chain and token pair that is not relayed gas-free', () => {
    const bscOrder = { ...order, chain: 'bsc', token: 'USDT' };
    expect(() => buildTransferAuthorization(offer, bscOrder, NONCE)).toThrow(/cannot be sent gas-free/);
  });

  it('refuses a non-authorization offer', () => {
    expect(() => buildTransferAuthorization({ ...offer, mode: 'topup' }, order, NONCE)).toThrow();
  });
});

describe('getSponsoredRoutes', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('returns routes when sponsorship is on', async () => {
    (global.fetch as jest.Mock).mockResolvedValue({
      ok: true,
      status: 200,
      json: async () => ({ success: true, data: { enabled: true, routes: [{ chain: 'bsc', token: 'USDT', mode: 'topup' }] } }),
    });
    await expect(getSponsoredRoutes()).resolves.toEqual([{ chain: 'bsc', token: 'USDT', mode: 'topup' }]);
  });

  it('returns [] when the request fails, so the sell screen keeps today behaviour', async () => {
    (global.fetch as jest.Mock).mockRejectedValue(new Error('offline'));
    await expect(getSponsoredRoutes()).resolves.toEqual([]);
  });

  it('matches routes case-insensitively', () => {
    expect(isGasSponsored([{ chain: 'bsc', token: 'USDT', mode: 'topup' }], 'BSC', 'usdt')).toBe(true);
    expect(isGasSponsored([{ chain: 'bsc', token: 'USDT', mode: 'topup' }], 'base', 'USDT')).toBe(false);
  });
});
