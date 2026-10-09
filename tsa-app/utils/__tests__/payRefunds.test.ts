import {
  defaultOption, merchantRefundLine, merchantRefundSubtitle, optionKey, refundBroadcastNote, refundFunding, refundStatusLine, sameAddress, sortRefundsOpenFirst, unprovenWalletMessage, wrongWalletMessage,
} from '../payRefunds';
import type { PayRefundDetail, PayRefundOption } from '@/services/payMerchantApi';
import type { WalletBalance } from '@/services/walletApi';

const USDC_BASE = '0x833589fCD6eDb6E08f4c7C32D4f71b54bdA02913';
const option: PayRefundOption = {
  chainId: 8453, chainKey: 'base', chainName: 'Base', tokenSymbol: 'USDC', tokenAddress: USDC_BASE, decimals: 6,
  amountBaseUnits: '12500000', amountDisplay: '12.5', isDefault: true,
};
const bal = (balance: string, contractAddress = USDC_BASE, symbol = 'USDC'): WalletBalance =>
  ({ symbol, name: symbol, balance, usdValue: '0', contractAddress, decimals: 6 });

describe('refund helpers', () => {
  it('puts the refunds waiting for her first, then the moving ones, then the rest newest first', () => {
    const rows = [
      { id: 'a', status: 'confirmed', createdAt: '2026-10-08T10:00:00Z' },
      { id: 'b', status: 'submitted', createdAt: '2026-10-01T10:00:00Z' },
      { id: 'c', status: 'requested', createdAt: '2026-10-02T10:00:00Z' },
      { id: 'd', status: 'failed', createdAt: '2026-10-09T10:00:00Z' },
      { id: 'e', status: 'requested', createdAt: '2026-10-05T10:00:00Z' },
    ] as const;
    expect(sortRefundsOpenFirst([...rows]).map((r) => r.id)).toEqual(['e', 'c', 'b', 'd', 'a']);
  });

  it('picks the customer\'s own network first, and keys an option by chain and token', () => {
    const other = { ...option, chainId: 56, chainKey: 'bsc', tokenSymbol: 'USDT', isDefault: false };
    expect(defaultOption([other, option])).toBe(option);
    expect(defaultOption([other])).toBe(other);
    expect(defaultOption([])).toBeUndefined();
    expect(optionKey(option)).toBe('8453:USDC');
  });

  it('compares wallets ignoring case, and never matches a missing one', () => {
    expect(sameAddress('0xAbC', '0xabc')).toBe(true);
    expect(sameAddress(null, '0xabc')).toBe(false);
    expect(sameAddress('0xabc', '0xabd')).toBe(false);
  });

  it('says which wallet to switch to', () => {
    expect(wrongWalletMessage('0x1111111111111111111111111111111111111111'))
      .toBe('This refund is sent from your TSA wallet 0x1111…1111. Switch to that wallet in Wallet → Manage Wallet, then come back.');
  });

  // Plans review I10: no wallet named yet is not a blank address.
  it('asks her to prove her wallet when the server names none', () => {
    expect(unprovenWalletMessage).toBe('Prove this wallet to send refunds. TSA Connect checks that the wallet on this device is yours before any money leaves it.');
  });

  it('says why the network has not taken a refund yet', () => {
    expect(refundBroadcastNote('Not enough gas in your wallet'))
      .toBe("Couldn't broadcast: Not enough gas in your wallet — top up gas; we retry automatically.");
  });

  it('reads her token balance against the exact amount', () => {
    expect(refundFunding(null, option)).toEqual({ kind: 'unknown' });
    expect(refundFunding([], option)).toEqual({ kind: 'empty' });
    expect(refundFunding([bal('0')], option)).toEqual({ kind: 'empty' });
    expect(refundFunding([bal('12.49')], option)).toEqual({ kind: 'short', have: '12.49' });
    expect(refundFunding([bal('40')], option)).toEqual({ kind: 'enough', have: '40' });
    // Found by symbol when the API sent no contract address.
    expect(refundFunding([bal('40', '')], option)).toEqual({ kind: 'enough', have: '40' });
  });

  // Review Focus 5.
  it('a balance with more decimals than the token still compares, never throws', () => {
    expect(refundFunding([bal('12.4999999')], option)).toEqual({ kind: 'short', have: '12.4999999' });
    expect(refundFunding([bal('12.5000009')], option)).toEqual({ kind: 'enough', have: '12.5000009' });
    expect(refundFunding([bal('not a number')], option)).toEqual({ kind: 'unknown' });
  });

  it('says where a refund stands once it is out of her hands', () => {
    const d = (status: string, over = {}) => ({
      payerFirstName: 'Ada',
      refund: { status, amount: '12.50', credited: '12.50', tokenSymbol: 'USDT', failureReason: 'The transaction failed on-chain', ...over },
    }) as unknown as PayRefundDetail;
    expect(refundStatusLine(d('submitted'))).toBe('Sent to Ada. Waiting for the network to confirm it.');
    // Plans review I6: the network refused the last send.
    expect(refundStatusLine(d('submitted', { lastBroadcastError: 'Not enough gas in your wallet' }))).toBe(
      "Sent to Ada, but the network hasn't taken it yet. Couldn't broadcast: Not enough gas in your wallet — top up gas; we retry automatically.");
    expect(refundStatusLine(d('confirmed'))).toBe('Ada received 12.50 USDT.');
    expect(refundStatusLine(d('failed'))).toBe('Nothing was refunded: The transaction failed on-chain. Ask for the refund again from your dashboard on the web.');
    expect(refundStatusLine(d('needs_review'))).toBe('TSA Connect is checking this transfer. We will be in touch.');
    expect(refundStatusLine(d('cancelled'))).toBe('This refund request was cancelled.');
    expect(refundStatusLine(d('requested'))).toBe("This refund can't be sent from the app right now. Please try again later.");
  });
});

describe('the payer\'s refund', () => {
  // Built from a LOCAL date so the day reads the same in every timezone.
  const r = { status: 'full' as const, amount: '12.50', tokenSymbol: 'USDT', chainId: 56, confirmedAt: new Date(2026, 9, 9, 12).toISOString() };
  it('reads as amount, token and day', () => {
    expect(merchantRefundLine(r)).toBe('12.50 USDT · 9 Oct');
    expect(merchantRefundLine({ ...r, confirmedAt: undefined })).toBe('12.50 USDT');
  });
  it('names a full, partial or pending refund in one line', () => {
    expect(merchantRefundSubtitle(r)).toBe('Refunded 12.50 USDT');
    expect(merchantRefundSubtitle({ ...r, status: 'partial', amount: '5.00' })).toBe('Partly refunded 5.00 USDT');
    expect(merchantRefundSubtitle({ status: 'pending', amount: '0.00' })).toBe('Refund on its way');
  });
});
