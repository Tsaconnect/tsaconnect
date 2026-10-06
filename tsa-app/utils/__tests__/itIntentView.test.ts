import { deliveryExplorerUrl, intentDisplayRateMinor, intentFeeLines } from '../itIntentView';

const HASH = `0x${'ab'.repeat(32)}`;

describe('intentFeeLines', () => {
  it('formats the stored fee lines the way the buy screen showed them (spec §14)', () => {
    expect(intentFeeLines([
      { kind: 'conversion', label: 'Flutterwave conversion fee (1.5%)', provider: 'Flutterwave', units: '1462160', ngnMinor: '247480' },
      { kind: 'network', label: 'Network fee', provider: 'Flutterwave', units: '1500000', ngnMinor: '253885' },
    ])).toEqual([
      { label: 'Flutterwave conversion fee (1.5%)', provider: 'Flutterwave', token: '1.46216', ngn: '2,474.80' },
      { label: 'Network fee', provider: 'Flutterwave', token: '1.5', ngn: '2,538.85' },
    ]);
  });

  it('is empty for an intent without fees, or with a malformed list', () => {
    expect(intentFeeLines(undefined)).toEqual([]);
    expect(intentFeeLines(null)).toEqual([]);
    expect(intentFeeLines({} as never)).toEqual([]);
    expect(intentFeeLines([{ label: 'x' } as never])).toEqual([]);
  });
});

describe('intentDisplayRateMinor', () => {
  it('shows the stored display rate, else the all-in one', () => {
    expect(intentDisplayRateMinor({ displayRate: '169257', rateKobo: '174561' })).toBe('169257');
    expect(intentDisplayRateMinor({ displayRate: '0', rateKobo: '174561' })).toBe('174561');
    expect(intentDisplayRateMinor({ rateKobo: '138257' })).toBe('138257');
  });
});

describe('deliveryExplorerUrl', () => {
  const settled = { provider: 'treasury', status: 'settled', txHash: HASH, destChain: 'BASE' };

  it('links a settled company-wallet delivery to its chain explorer', () => {
    expect(deliveryExplorerUrl(settled)).toBe(`https://basescan.org/tx/${HASH}`);
  });

  it('offers nothing before delivery, for another provider, or for a non-hash', () => {
    expect(deliveryExplorerUrl({ ...settled, status: 'delivering' })).toBeUndefined();
    expect(deliveryExplorerUrl({ ...settled, provider: 'flutterwave' })).toBeUndefined();
    expect(deliveryExplorerUrl({ ...settled, provider: undefined })).toBeUndefined();
    expect(deliveryExplorerUrl({ ...settled, txHash: '../address/0x1' })).toBeUndefined();
    expect(deliveryExplorerUrl({ ...settled, destChain: undefined })).toBeUndefined();
  });
});
