/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * After a payment fails, the way out depends on whether orders exist. Once
 * they do, the cart they came from is gone: "Back to checkout" would find
 * nothing to pay for, so the buyer must be able to resume those orders.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

jest.mock('@/components/ui', () => {
  const buttons: any[] = [];
  const Pass = ({ children }: any) => children ?? null;
  return {
    __esModule: true,
    __buttons: buttons,
    Button: (props: any) => { buttons.push(props); return null; },
    Card: Pass,
    Icon: () => null,
    Spinner: () => null,
    Text: () => null,
  };
});

import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';
import { PaymentProgress, type PaymentProgressProps } from '../PaymentProgress';

const buttons = (jest.requireMock('@/components/ui') as any).__buttons as any[];

const titlesFor = (props: Partial<PaymentProgressProps>) => {
  buttons.length = 0;
  act(() => {
    TestRenderer.create(
      <PaymentProgress
        token="USDC"
        orders={[{ name: 'Durable lamp', amount: '0.18 USDC' }]}
        phase="creating_escrow"
        currentOrderIndex={0}
        error="Not enough USDC in your wallet."
        completedCount={0}
        onBackToCheckout={() => {}}
        onViewOrders={() => {}}
        {...props}
      />,
    );
  });
  return buttons.map((b) => b.title);
};

test('a failure after orders were created offers to resume them, not to go back to an empty cart', () => {
  const onRetry = jest.fn();
  expect(titlesFor({ onRetry })).toEqual(['Try again', 'View orders']);
  buttons.find((b) => b.title === 'Try again').onPress();
  expect(onRetry).toHaveBeenCalled();
});

test('a failure before any order exists sends the buyer back to checkout', () => {
  expect(titlesFor({})).toEqual(['Back to checkout']);
});

test('once some orders are paid and none can be resumed, the buyer is sent to their orders', () => {
  expect(titlesFor({ completedCount: 1 })).toEqual(['View orders']);
});

test('once orders exist and none can be resumed, the buyer is sent to their orders, not back to an empty cart', () => {
  expect(titlesFor({ ordersCreated: true })).toEqual(['View orders']);
});
