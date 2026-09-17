// utils/__tests__/itBuyOrder.test.ts
//
// The one-open-order refusal and the cancel flow for Instant Transfer MCGP buys.
// Codes are asserted literally: they must match the Go API
// (internal/handlers/it_handler.go itBuyOrderOpenCode / itBuyPaymentArrivedCode),
// and a drifted spelling fails silently — the user gets a dead-end error instead
// of being sent to the order they already have.

// jest.mock calls below are hoisted above this import by babel-jest.
import {
  IT_BUY_ORDER_OPEN,
  IT_BUY_PAYMENT_ARRIVED,
  cancelBuyErrorMessage,
  confirmCancelITBuy,
  itIntentRoute,
  openBuyOrderOf,
} from '../itBuyOrder';

const mockAlert = jest.fn();

jest.mock('react-native', () => ({
  Alert: { alert: (...args: unknown[]) => mockAlert(...args) },
}));

beforeEach(() => {
  mockAlert.mockReset();
});

describe('IT buy refusal codes', () => {
  it('match the server spelling', () => {
    expect(IT_BUY_ORDER_OPEN).toBe('it_buy_order_open');
    expect(IT_BUY_PAYMENT_ARRIVED).toBe('it_buy_payment_arrived');
  });
});

describe('openBuyOrderOf', () => {
  it('reads the open order the 409 names', () => {
    expect(
      openBuyOrderOf({ code: IT_BUY_ORDER_OPEN, errorData: { intentId: 'abc', ngnKobo: '500000' } }),
    ).toEqual({ intentId: 'abc', ngnKobo: '500000' });
  });

  it('is null for any other refusal, so it never swallows one', () => {
    expect(openBuyOrderOf({ code: 'kyc_verification_required', errorData: { intentId: 'abc' } })).toBeNull();
    expect(openBuyOrderOf({ code: undefined })).toBeNull();
  });

  it('is null when the server named no order — there is nowhere to route', () => {
    expect(openBuyOrderOf({ code: IT_BUY_ORDER_OPEN })).toBeNull();
    expect(openBuyOrderOf({ code: IT_BUY_ORDER_OPEN, errorData: { intentId: '' } })).toBeNull();
    expect(openBuyOrderOf({ code: IT_BUY_ORDER_OPEN, errorData: 'nope' })).toBeNull();
  });

  it('tolerates a missing amount', () => {
    expect(openBuyOrderOf({ code: IT_BUY_ORDER_OPEN, errorData: { intentId: 'abc' } })).toEqual({
      intentId: 'abc',
      ngnKobo: '',
    });
  });
});

describe('itIntentRoute', () => {
  it('is a string-template URL, not a pathname object', () => {
    expect(itIntentRoute('abc')).toBe('/wallet/it/intent/abc');
  });
});

describe('cancelBuyErrorMessage', () => {
  it('shows the server explanation when the payment has already arrived', () => {
    expect(cancelBuyErrorMessage({ code: IT_BUY_PAYMENT_ARRIVED, message: 'A payment arrived.' })).toBe(
      'A payment arrived.',
    );
  });

  it('says the order has moved on when the server reports a bad state, rather than inviting a retry', () => {
    expect(cancelBuyErrorMessage({ code: 'stable_bad_state', message: 'invalid IT intent state' })).toBe(
      'This order can no longer be cancelled — its status has changed.',
    );
  });

  it('falls back to a retry prompt otherwise', () => {
    expect(cancelBuyErrorMessage({ code: undefined, message: undefined })).toBe('Could not cancel. Try again.');
  });
});

describe('confirmCancelITBuy', () => {
  it('asks before cancelling, and says money sent is refunded rather than credited', () => {
    const onConfirm = jest.fn();
    confirmCancelITBuy(onConfirm);

    expect(mockAlert).toHaveBeenCalledTimes(1);
    const [title, message, buttons] = mockAlert.mock.calls[0];
    expect(title).toBe('Cancel this order?');
    expect(message).toMatch(/NOT sent the money/);
    expect(message).toMatch(/held for review and refunded, not credited/);
    expect(buttons.map((b: { text: string }) => b.text)).toEqual(['Keep order', 'Cancel order']);
    expect(buttons[0].style).toBe('cancel');
    expect(buttons[1].style).toBe('destructive');
    expect(onConfirm).not.toHaveBeenCalled();

    buttons[1].onPress();
    expect(onConfirm).toHaveBeenCalledTimes(1);
  });
});
