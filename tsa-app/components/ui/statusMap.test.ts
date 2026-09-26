import { STATUS_MAP, getStatusVisual, type StatusTone } from './statusMap';

const VALID_TONES: StatusTone[] = ['success', 'warning', 'danger', 'info', 'escrow', 'accent', 'neutral'];

describe('P2P status map', () => {
  it('covers every trade status + derived states', () => {
    const expected = [
      'pending_payment',
      'paid',
      'releasing',
      'released',
      'cancelled',
      'disputed',
      'resolved',
      'expired',
    ];
    expect(Object.keys(STATUS_MAP.trade).sort()).toEqual(expected.sort());
  });

  it('covers every ad status + derived states', () => {
    const expected = ['pending', 'active', 'paused', 'exhausted', 'cancelled', 'rateStale'];
    expect(Object.keys(STATUS_MAP.ad).sort()).toEqual(expected.sort());
  });

  it('covers every dispute status', () => {
    expect(Object.keys(STATUS_MAP.dispute).sort()).toEqual(['open', 'rejected', 'resolved'].sort());
  });

  it('covers every payment-link status + cancelled', () => {
    expect(Object.keys(STATUS_MAP.payLink).sort()).toEqual(
      ['pending', 'processing', 'succeeded', 'failed', 'expired', 'cancelled'].sort(),
    );
    expect(getStatusVisual('payLink', 'cancelled').label).toBe('Cancelled');
    expect(getStatusVisual('payLink', 'expired').label).toBe('Expired');
  });

  it('disambiguates cross-namespace collisions (cancelled / resolved)', () => {
    expect(getStatusVisual('trade', 'released').label).toBe('Completed');
    expect(getStatusVisual('trade', 'resolved').label).toBe('Resolved');
    expect(getStatusVisual('dispute', 'resolved').tone).toBe('success');
    expect(getStatusVisual('trade', 'resolved').tone).toBe('info');
  });

  it('every mapped tone is a known tone', () => {
    for (const kind of Object.values(STATUS_MAP)) {
      for (const visual of Object.values(kind)) {
        expect(VALID_TONES).toContain(visual.tone);
        expect(visual.label.length).toBeGreaterThan(0);
      }
    }
  });

  it('falls back to a neutral pill for unknown statuses', () => {
    const v = getStatusVisual('trade', 'something_new');
    expect(v.tone).toBe('neutral');
    expect(v.label).toBe('something new');
  });

  it('releasing maps to the escrow tone (off-chain intermediate)', () => {
    expect(getStatusVisual('trade', 'releasing').tone).toBe('escrow');
  });
});
