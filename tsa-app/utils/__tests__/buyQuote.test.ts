import { buyFeeBreakdown } from '../buyQuote';

// ₦1.00 per MCGP keeps the arithmetic readable: kobo/100 = MCGP.
const RATE_1_NAIRA = '100';
const wei = (mcgp: string) => (BigInt(Math.round(Number(mcgp) * 1e4)) * 10n ** 14n).toString();

describe('buyFeeBreakdown', () => {
  // The PM's framing: "let the users see that they are paying for e.g 10mcgp
  // but will receive 8mcgp as 2mcgp will cover the fees".
  it('splits the purchase into gross, fee and net MCGP', () => {
    const b = buyFeeBreakdown({
      ngnKobo: '1000',
      mcgpAmount: wei('8'),
      rateKobo: RATE_1_NAIRA,
      feeNgnKobo: '200',
      netNgnKobo: '800',
    });
    expect(b.hasFees).toBe(true);
    expect(b.grossMcgp).toBe('10');
    expect(b.feeMcgp).toBe('2');
    expect(b.netMcgp).toBe('8');
  });

  it('always reconciles: net + fee equals gross', () => {
    const b = buyFeeBreakdown({
      ngnKobo: '50000',
      mcgpAmount: wei('486.7'),
      rateKobo: RATE_1_NAIRA,
      feeNgnKobo: '1330',
      netNgnKobo: '48670',
    });
    expect(b.grossMcgp).toBe('500');
    expect(b.feeMcgp).toBe('13.3');
    expect(b.netMcgp).toBe('486.7');
    expect(Number(b.netMcgp) + Number(b.feeMcgp)).toBeCloseTo(Number(b.grossMcgp), 4);
  });

  it('reports the naira fee alongside, for the secondary line', () => {
    const b = buyFeeBreakdown({
      ngnKobo: '50000',
      mcgpAmount: wei('486.7'),
      rateKobo: RATE_1_NAIRA,
      feeNgnKobo: '1330',
      netNgnKobo: '48670',
    });
    expect(b.feeNgn).toBe('13.30');
  });

  // An API that predates fee deduction returns no fee fields at all. Showing a
  // "0 MCGP fee" breakdown there would be a lie, so the screen must be able to
  // omit the block entirely.
  it('reports no fees when the quote carries no fee fields', () => {
    const b = buyFeeBreakdown({
      ngnKobo: '50000',
      mcgpAmount: wei('500'),
      rateKobo: RATE_1_NAIRA,
    });
    expect(b.hasFees).toBe(false);
    expect(b.netMcgp).toBe('500');
  });

  // A zero or unparseable credited amount would otherwise render "you pay for
  // 500 MCGP, fees −500 MCGP, you receive 0" — a 100% fee. Omit the breakdown
  // instead; a fee display that can't be trusted is worse than none.
  it('omits the breakdown rather than showing a 100% fee', () => {
    for (const mcgpAmount of ['0', 'abc', '']) {
      const b = buyFeeBreakdown({
        ngnKobo: '50000',
        mcgpAmount,
        rateKobo: RATE_1_NAIRA,
        feeNgnKobo: '1330',
        netNgnKobo: '48670',
      });
      expect(b.hasFees).toBe(false);
      expect(b.feeMcgp).toBe('0');
    }
  });

  it('degrades safely on an unusable rate rather than dividing by zero', () => {
    for (const rateKobo of ['0', '', 'abc']) {
      const b = buyFeeBreakdown({
        ngnKobo: '50000',
        mcgpAmount: wei('500'),
        rateKobo,
        feeNgnKobo: '1330',
        netNgnKobo: '48670',
      });
      expect(b.hasFees).toBe(false);
      expect(b.netMcgp).toBe('500');
    }
  });

  // Display TRUNCATES at 4dp rather than rounding: an asset figure must never
  // promise more MCGP than the user actually has. ₦500 ÷ ₦27.89 = 17.92757…,
  // which shows as 17.9275, not 17.9276.
  it('truncates display at 4dp rather than overstating the asset', () => {
    const b = buyFeeBreakdown({
      ngnKobo: '50000',
      mcgpAmount: '17450699175331660', // net ₦486.70 worth
      rateKobo: '2789',
      feeNgnKobo: '1330',
      netNgnKobo: '48670',
    });
    expect(b.grossMcgp).toBe('17.9275');
    expect(Number(b.feeMcgp)).toBeGreaterThan(0);
  });
});
