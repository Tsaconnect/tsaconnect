// services/__tests__/disputesApi.test.ts
//
// The client for the one dispute queue that covers every kind of order. The
// behaviour worth pinning is the subject lookup: a 404 there is an ORDINARY
// answer ("you have not disputed this order"), not a failure, because it is
// what the screens use to choose between offering the form and linking to the
// dispute already filed. Treating it as an error would show an error banner on
// every undisputed order in the app.
//
// The AsyncStorage mock is required — see paycrestApi.claim.test.ts.
jest.mock('@react-native-async-storage/async-storage', () =>
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));

import {
  fileDispute,
  getDisputeForSubject,
  disputeAffordance,
  disputeStatusLabel,
  isDisputeOpen,
} from '../disputesApi';

const okJson = (data: unknown) => ({
  ok: true,
  status: 200,
  json: async () => ({ success: true, data }),
});

const errJson = (status: number, message: string) => ({
  ok: false,
  status,
  json: async () => ({ success: false, message }),
});

describe('fileDispute', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('POSTs the subject, reason and evidence', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: 'd1', status: 'open' }));

    await fileDispute({
      subjectType: 'paycrest_order',
      subjectId: 'order-1',
      reason: 'Paid, never credited',
      evidenceUrls: ['https://cdn/a.jpg'],
    });

    const [url, init] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('https://tsa.mcgpchain.com/api/disputes');
    expect(init.method).toBe('POST');
    expect(JSON.parse(init.body)).toEqual({
      subjectType: 'paycrest_order',
      subjectId: 'order-1',
      reason: 'Paid, never credited',
      evidenceUrls: ['https://cdn/a.jpg'],
    });
  });

  it('surfaces the server message so the screen can show why it refused', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(
      errJson(409, 'You have already opened a dispute on this order.'),
    );

    const res = await fileDispute({
      subjectType: 'bill_order',
      subjectId: 'order-1',
      reason: 'x',
    });

    expect(res.success).toBe(false);
    expect(res.message).toBe('You have already opened a dispute on this order.');
  });
});

describe('getDisputeForSubject', () => {
  beforeEach(() => {
    (global as any).fetch = jest.fn();
  });

  it('asks the subject-scoped route', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(okJson({ id: 'd1', status: 'open' }));

    await getDisputeForSubject('it_intent', 'intent-9');

    const [url] = (global.fetch as jest.Mock).mock.calls[0];
    expect(url).toBe('https://tsa.mcgpchain.com/api/disputes/subject/it_intent/intent-9');
  });

  it('reports "no dispute" as null rather than an error', async () => {
    (global.fetch as jest.Mock).mockResolvedValue(errJson(404, 'Not found'));

    const res = await getDisputeForSubject('paycrest_order', 'order-1');

    expect(res.success).toBe(true);
    expect(res.data).toBeNull();
  });

  it('still reports a real failure as a failure', async () => {
    // A 500 must not be mistaken for "nothing filed" — that would offer the
    // form again and let the user file a second dispute.
    (global.fetch as jest.Mock).mockResolvedValue(errJson(500, 'Something went wrong.'));

    const res = await getDisputeForSubject('paycrest_order', 'order-1');

    expect(res.success).toBe(false);
  });
});

describe('status helpers', () => {
  it('treats escalated as still open, because the user is still waiting', () => {
    expect(isDisputeOpen('open')).toBe(true);
    expect(isDisputeOpen('escalated')).toBe(true);
    expect(isDisputeOpen('resolved')).toBe(false);
    expect(isDisputeOpen('rejected')).toBe(false);
  });

  it('labels escalated in terms of what is happening, not our jargon', () => {
    expect(disputeStatusLabel('open')).toBe('Under review');
    expect(disputeStatusLabel('escalated')).toBe('With the payment provider');
    expect(disputeStatusLabel('resolved')).toBe('Resolved');
    expect(disputeStatusLabel('rejected')).toBe('Closed');
  });
});

describe('disputeAffordance', () => {
  it('shows the existing dispute once one is known', () => {
    expect(disputeAffordance({ lookup: 'found', disputable: true })).toBe('show-dispute');
    // Even on an order too late to dispute, a dispute already filed must stay
    // visible — it is where the user reads the outcome.
    expect(disputeAffordance({ lookup: 'found', disputable: false })).toBe('show-dispute');
  });

  it('offers the form when the order is disputable and nothing is filed', () => {
    expect(disputeAffordance({ lookup: 'none', disputable: true })).toBe('offer');
    expect(disputeAffordance({ lookup: 'none', disputable: false })).toBe('hidden');
  });

  it('hides nothing while the lookup is still in flight', () => {
    expect(disputeAffordance({ lookup: 'pending', disputable: true })).toBe('hidden');
  });

  it('still offers the form when the lookup failed', () => {
    // The earlier behaviour hid the affordance entirely on a failed lookup,
    // leaving a user with no way to report a problem and nothing on screen
    // explaining why. The real guard against a duplicate is the server's
    // unique index, which refuses with a message the form can show.
    expect(disputeAffordance({ lookup: 'failed', disputable: true })).toBe('offer');
  });
});
