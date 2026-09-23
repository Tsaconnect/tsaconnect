// toLiFiQuote maps a raw LiFi quote body — including the one a sell intent
// hands back untouched — into the app's LiFiQuote. TSA Pay needs the amount the
// route actually pulls from the payer's wallet, which an exact-output quote
// computes on LiFi's side.
import { toLiFiQuote } from '../lifi';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));
jest.mock('../../hooks/useNetwork', () => ({ getNetwork: () => 'mainnet' }));

describe('toLiFiQuote fromAmount', () => {
  it('reads what the route spends from action.fromAmount', () => {
    const q = toLiFiQuote({ action: { fromAmount: '84000000000000000' }, estimate: { toAmount: '33500000' } });
    expect(q.fromAmount).toBe('84000000000000000');
  });

  it('falls back to estimate.fromAmount', () => {
    const q = toLiFiQuote({ estimate: { fromAmount: '5', toAmount: '1' } });
    expect(q.fromAmount).toBe('5');
  });

  it('is absent, not "0", when the body carries none', () => {
    expect(toLiFiQuote({ estimate: { toAmount: '1' } }).fromAmount).toBeUndefined();
  });
});
