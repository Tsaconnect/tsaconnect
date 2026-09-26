import { payProgressSteps, type PayProgressInput } from '../payProgress';

const base: PayProgressInput = { stage: 'paying', swapStage: 'idle', swapped: false, hasOrder: true, merchantName: 'Acme' };
const waiting: PayProgressInput = { ...base, stage: 'waiting' };
const states = (steps?: { key: string; state: string }[]) => steps?.map((s) => `${s.key}:${s.state}`);

describe('payProgressSteps', () => {
  it('shows nothing while the attempt is not moving — the panel asks her instead', () => {
    for (const stage of ['idle', 'continue', 'deposit', 'shortfall', 'failed'] as const) {
      expect(payProgressSteps({ ...base, stage })).toBeUndefined();
    }
  });

  it('shows nothing before the attempt has taken a shape, so the list never reshapes under her', () => {
    // Just after Pay: no swap reported yet, no order opened. Either could come next.
    expect(payProgressSteps({ ...base, hasOrder: false })).toBeUndefined();
  });

  it('sends a direct payment from her wallet once its order exists', () => {
    const steps = payProgressSteps(base);
    expect(states(steps)).toEqual(['sent:active', 'received:pending', 'paying:pending', 'paid:pending']);
    expect(steps?.[0].detail).toBe('Sending from your wallet');
    expect(steps?.[2].label).toBe('Paying Acme');
  });

  it('puts the swap first when she pays with another token, and keeps it once past', () => {
    const swapped = { ...base, swapped: true, hasOrder: false };
    expect(states(payProgressSteps({ ...swapped, swapStage: 'swapping' }))?.slice(0, 2)).toEqual(['swap:active', 'sent:pending']);
    expect(payProgressSteps({ ...swapped, swapStage: 'waiting' })?.[0].detail).toBe('This can take a few minutes');
    expect(states(payProgressSteps({ ...swapped, swapStage: 'sending' }))?.slice(0, 2)).toEqual(['swap:done', 'sent:active']);
    expect(payProgressSteps({ ...swapped, swapStage: 'ordering' })?.[1].detail).toBe('Opening the payout');
  });

  it('waits on the provider until the order says otherwise', () => {
    expect(states(payProgressSteps(waiting))).toEqual(['sent:done', 'received:active', 'paying:pending', 'paid:pending']);
    expect(states(payProgressSteps({ ...waiting, orderStatus: 'awaiting_deposit' }))?.[1]).toBe('received:active');
    expect(states(payProgressSteps({ ...waiting, orderStatus: 'pending' }))?.[2]).toBe('paying:active');
    expect(states(payProgressSteps({ ...waiting, orderStatus: 'settling' }))?.[2]).toBe('paying:active');
    expect(payProgressSteps({ ...waiting, orderStatus: 'settled' })?.every((s) => s.state === 'done')).toBe(true);
  });

  it('a send whose outcome is unknown is still being checked, never "sent"', () => {
    const steps = payProgressSteps({ ...waiting, notice: "We couldn't confirm whether your payment was sent." });
    expect(states(steps)?.slice(0, 2)).toEqual(['sent:active', 'received:pending']);
    expect(steps?.[0].detail).toBe('Checking whether it left your wallet');
  });

  it('a wait with no order to read shows no steps — nothing could ever move them', () => {
    expect(payProgressSteps({ ...waiting, hasOrder: false })).toBeUndefined();
  });

  it('leaves an order that ended badly to the checkout, not a progress list', () => {
    for (const orderStatus of ['expired', 'failed', 'refunded', 'unmatched'] as const) {
      expect(payProgressSteps({ ...waiting, orderStatus })).toBeUndefined();
    }
  });
});
