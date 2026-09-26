import { paycrestOrderSteps } from '../paycrestOrderSteps';
import type { PaycrestStatus } from '@/services/paycrestStatus';

const when = (iso: string) => `at ${iso}`;
const order = (status: PaycrestStatus, extra: Record<string, string> = {}) => ({
  direction: 'sell' as const,
  status,
  createdAt: 'T0',
  ...extra,
});
const states = (steps: { key: string; state: string }[]) => steps.map((s) => `${s.key}:${s.state}`);

describe('paycrestOrderSteps', () => {
  it('waits on her deposit until she (or this phone) has sent it', () => {
    expect(states(paycrestOrderSteps({ order: order('awaiting_deposit'), when }))).toEqual([
      'opened:done', 'sent:active', 'received:pending', 'paying:pending', 'paid:pending',
    ]);
    const sent = paycrestOrderSteps({ order: order('awaiting_deposit'), sent: 'proven', when });
    expect(states(sent)).toEqual(['opened:done', 'sent:done', 'received:active', 'paying:pending', 'paid:pending']);
  });

  it('a deposit that only may have left is being confirmed, never drawn as sent', () => {
    const steps = paycrestOrderSteps({ order: order('awaiting_deposit'), sent: 'unconfirmed', when });
    expect(states(steps).slice(0, 3)).toEqual(['opened:done', 'sent:active', 'received:pending']);
    expect(steps[1].detail).toBe('Confirming it left your wallet');
    // Nor does it count as sent on a failure.
    expect(states(paycrestOrderSteps({ order: order('failed'), sent: 'unconfirmed', when }))).toEqual(['opened:done', 'failed:failed']);
  });

  it('takes a claim as her side done, with its time', () => {
    const steps = paycrestOrderSteps({ order: order('awaiting_deposit', { paymentClaimedAt: 'T1' }), when });
    expect(steps[1]).toMatchObject({ state: 'done', detail: 'at T1' });
    expect(steps[2].state).toBe('active');
  });

  it('walks the provider statuses forward', () => {
    expect(states(paycrestOrderSteps({ order: order('pending'), when })).slice(2)).toEqual([
      'received:done', 'paying:active', 'paid:pending',
    ]);
    expect(paycrestOrderSteps({ order: order('pending'), when })[3].detail).toBe('Matching a provider');
    expect(states(paycrestOrderSteps({ order: order('settling'), when })).slice(3)).toEqual(['paying:active', 'paid:pending']);
  });

  it('shows a settled order all done, with when it opened and when it paid', () => {
    const steps = paycrestOrderSteps({ order: order('settled', { settledAt: 'T9' }), when });
    expect(steps.every((s) => s.state === 'done')).toBe(true);
    expect(steps[0].detail).toBe('at T0');
    expect(steps[4]).toMatchObject({ label: 'Paid to your bank', detail: 'at T9' });
  });

  it('labels a buy by what moves on a buy', () => {
    const steps = paycrestOrderSteps({ order: { ...order('settled'), direction: 'buy' }, when });
    expect(steps.map((s) => s.label)).toEqual([
      'Order opened', 'Transfer sent', 'Received by the provider', 'Sending your crypto', 'Delivered to your wallet',
    ]);
  });

  it('ends a failure at the step it happened on, with nothing pending after it', () => {
    expect(states(paycrestOrderSteps({ order: order('expired'), when }))).toEqual(['opened:done', 'expired:failed']);
    expect(states(paycrestOrderSteps({ order: order('cancelled'), when }))).toEqual(['opened:done', 'cancelled:failed']);
    expect(states(paycrestOrderSteps({ order: order('failed'), sent: 'proven', when }))).toEqual([
      'opened:done', 'sent:done', 'failed:failed',
    ]);
  });

  it('shows a stuck payment as being recovered, never as failed', () => {
    for (const status of ['unmatched', 'recovery_pending'] as const) {
      expect(states(paycrestOrderSteps({ order: order(status), when }))).toEqual([
        'opened:done', 'sent:done', 'recovery:active',
      ]);
    }
  });

  it('shows a refund returning, then returned', () => {
    expect(states(paycrestOrderSteps({ order: order('refunding'), when })).at(-1)).toBe('refund:active');
    expect(states(paycrestOrderSteps({ order: order('refunded'), when })).at(-1)).toBe('refund:done');
  });

  it('claims only the opening for a status this build does not know', () => {
    expect(states(paycrestOrderSteps({ order: order('brand_new' as PaycrestStatus), when }))).toEqual(['opened:done']);
  });
});
