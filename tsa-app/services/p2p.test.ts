jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

import { distinctAdProviders, type P2PAd } from './p2p';

function ad(summary: { provider: string; label: string }[]): P2PAd {
  return { paymentMethodSummary: summary } as unknown as P2PAd;
}

describe('distinctAdProviders', () => {
  it('returns deduped providers across ads, order-stable', () => {
    const ads = [
      ad([{ provider: 'bank_transfer', label: 'Bank Transfer' }, { provider: 'opay', label: 'OPay' }]),
      ad([{ provider: 'opay', label: 'OPay' }, { provider: 'paypal', label: 'PayPal' }]),
    ];
    expect(distinctAdProviders(ads)).toEqual([
      { provider: 'bank_transfer', label: 'Bank Transfer' },
      { provider: 'opay', label: 'OPay' },
      { provider: 'paypal', label: 'PayPal' },
    ]);
  });

  it('ignores ads with no summary', () => {
    expect(distinctAdProviders([ad([]), {} as P2PAd])).toEqual([]);
  });
});
