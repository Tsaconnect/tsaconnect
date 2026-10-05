// The fee terms a payment merchant reads before posting an ad. The numbers come
// from GET /p2p/pm/me — the same schedule the server deducts — so this only
// has to say them plainly and never invent one.
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

// eslint-disable-next-line import/first
import { describePayoutFees } from '../pmPayoutFees';

const flutterwave = {
  fundingFeeBps: 140,
  transferFeeTiers: [
    { upToNgn: '5000.00', feeNgn: '10.75' },
    { upToNgn: '50000.00', feeNgn: '26.88' },
    { upToNgn: null, feeNgn: '53.75' },
  ],
  minPayoutNgn: '100.00',
};

describe('describePayoutFees', () => {
  it('states the funding percentage and every transfer band', () => {
    expect(describePayoutFees(flutterwave)).toBe(
      'Each payout to your bank is reduced by Flutterwave’s costs: 1.4% of what the buyer pays, ' +
        'plus a transfer fee of ₦10.75 up to ₦5,000.00, ₦26.88 up to ₦50,000.00, or ₦53.75 above that. ' +
        'A trade that would leave less than ₦100.00 after fees is not paid out automatically.'
    );
  });

  it('handles a single flat band', () => {
    const text = describePayoutFees({ ...flutterwave, transferFeeTiers: [{ upToNgn: null, feeNgn: '50.00' }] });
    expect(text).toContain('plus a transfer fee of ₦50.00.');
  });

  it('omits a zero funding fee rather than showing 0%', () => {
    const text = describePayoutFees({ ...flutterwave, fundingFeeBps: 0 });
    expect(text).not.toContain('%');
    expect(text).toContain('a transfer fee of ₦10.75 up to ₦5,000.00');
  });
});
