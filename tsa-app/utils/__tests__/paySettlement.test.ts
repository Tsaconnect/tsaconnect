jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
/* eslint-disable import/first -- jest hoists the mock above the imports it replaces. */
import { describeSettlement, settlementChangeFrom, settlesIn } from '../paySettlement';
import type { OwnMerchant } from '@/services/payMerchantApi';

const USDC_BASE = { address: '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913', symbol: 'USDC', decimals: 6, chainId: 8453 } as any;
const WALLET = '0x52908400098527886E0F7030069857D2E4169EE7';

describe('settlesIn — what a merchant can be paid in', () => {
  it('an ERC-20 on a chain the app knows', () => {
    expect(settlesIn(8453, USDC_BASE)).toBe(true);
  });
  it('never the chain’s own coin, nor a chain the app does not know', () => {
    expect(settlesIn(8453, { address: '0x0000000000000000000000000000000000000000' })).toBe(false);
    expect(settlesIn(999_999, USDC_BASE)).toBe(false);
  });
});

describe('settlementChangeFrom', () => {
  it('a complete bank account is sent as it was entered', () => {
    expect(settlementChangeFrom({ mode: 'fiat', bankCode: '044', accountNumber: ' 0123456789 ' }))
      .toEqual({ change: { settlementMode: 'fiat', bankCode: '044', accountNumber: '0123456789' } });
  });
  it.each([
    [{ mode: 'fiat', accountNumber: '0123456789' }, /bank/i],
    [{ mode: 'fiat', bankCode: '044', accountNumber: '12345' }, /10-digit/],
    [{ mode: 'crypto', walletAddress: WALLET }, /token/i],
    [{ mode: 'crypto', token: USDC_BASE, walletAddress: 'not-an-address' }, /wallet address/i],
    [{ mode: 'crypto', token: { ...USDC_BASE, address: '0x0000000000000000000000000000000000000000', symbol: 'ETH' }, walletAddress: WALLET }, /ETH/],
  ])('%#: says what is missing', (form, why) => {
    const out = settlementChangeFrom(form as any);
    expect('missing' in out && out.missing).toMatch(why);
  });
  it('a complete wallet is sent with the token and chain it was picked on', () => {
    expect(settlementChangeFrom({ mode: 'crypto', token: USDC_BASE, walletAddress: WALLET })).toEqual({
      change: { settlementMode: 'crypto', chainId: 8453, tokenAddress: USDC_BASE.address, tokenSymbol: 'USDC', walletAddress: WALLET },
    });
  });
});

describe('describeSettlement', () => {
  const base = { id: 'm1', businessName: 'Sky Air', status: 'active' } as OwnMerchant;
  it('a bank account by name, bank and masked number', () => {
    const m = { ...base, settlementMode: 'fiat', bankCode: '044', accountNumber: '******3210', accountName: 'SKY AIR LTD' } as OwnMerchant;
    expect(describeSettlement(m, (c) => (c === '044' ? 'Access Bank' : undefined))).toBe('SKY AIR LTD · Access Bank ******3210');
  });
  it('a wallet by token, network and a shortened address', () => {
    const m = { ...base, settlementMode: 'crypto', chainId: 8453, tokenSymbol: 'USDC', walletAddress: WALLET } as OwnMerchant;
    expect(describeSettlement(m)).toBe('USDC on Base to 0x5290…9EE7');
  });
  it('nothing set up yet', () => {
    expect(describeSettlement({ ...base, settlementMode: '' } as OwnMerchant)).toBe('Not set up yet');
  });
});
