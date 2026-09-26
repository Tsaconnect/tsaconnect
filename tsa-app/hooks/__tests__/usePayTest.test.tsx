/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

// Probe-component pattern of useMerchantPricing.hook.test.tsx / the async
// flush pattern of useCheckoutQuote.test.tsx. payApi is mocked wholesale so
// the hook's own AsyncStorage-touching import chain (payApi -> apiRequest ->
// walletApi -> AsyncStorage) never runs in this test.
jest.mock('@/services/payApi', () => {
  class PayError extends Error {
    status: number;
    constructor(message: string, status: number) {
      super(message);
      this.name = 'PayError';
      this.status = status;
    }
  }
  return { __esModule: true, PayError, submitTestOutcome: jest.fn() };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { PayError, submitTestOutcome, type PayCheckout } from '@/services/payApi';
import { usePayTest, type UsePayTest } from '../usePayTest';

const mockSubmit = submitTestOutcome as jest.Mock;

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const checkout = { id: ID, status: 'pending' } as PayCheckout;

let hook: UsePayTest;
const Harness = () => {
  hook = usePayTest(ID);
  return null;
};

let renderer: ReactTestRenderer;
const mount = () => act(() => { renderer = TestRenderer.create(<Harness />); });
const flush = () => act(async () => { await Promise.resolve(); await Promise.resolve(); });

afterEach(() => {
  act(() => renderer?.unmount());
  mockSubmit.mockReset();
});

it('request only opens the modal — submitTestOutcome is not called yet', () => {
  mount();
  act(() => hook.request('approve'));
  expect(hook.authorization.visible).toBe(true);
  expect(mockSubmit).not.toHaveBeenCalled();
});

it('onCancel closes the modal without ever calling submitTestOutcome', () => {
  mount();
  act(() => hook.request('approve'));
  act(() => hook.authorization.onCancel());
  expect(hook.authorization.visible).toBe(false);
  expect(mockSubmit).not.toHaveBeenCalled();
});

it('onAuthorized submits the requested outcome exactly once and sets result', async () => {
  const updated = { ...checkout, status: 'succeeded' } as PayCheckout;
  mockSubmit.mockResolvedValue(updated);

  mount();
  act(() => hook.request('approve'));
  act(() => hook.authorization.onAuthorized());
  await flush();

  expect(mockSubmit).toHaveBeenCalledTimes(1);
  expect(mockSubmit).toHaveBeenCalledWith(ID, 'approve');
  expect(hook.result).toEqual(updated);
});

it('surfaces a rejected PayError message', async () => {
  mockSubmit.mockRejectedValue(new PayError('Someone else is completing this payment', 409));

  mount();
  act(() => hook.request('approve'));
  act(() => hook.authorization.onAuthorized());
  await flush();

  expect(hook.error).toBe('Someone else is completing this payment');
  expect(hook.submitting).toBe(false);
});
