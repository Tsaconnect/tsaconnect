/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
// The market's KYC state shows kycRefusalFor's copy (no copy of its own).
const mockEmptyState = jest.fn((_props: any) => null);
jest.mock('@/components/ui', () => ({ __esModule: true, EmptyState: (p: any) => mockEmptyState(p) }));
const mockPush = jest.fn();
jest.mock('expo-router', () => ({ router: { push: (...a: any[]) => mockPush(...a) } }));

import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';
import { KycRequiredState } from '../KycRequiredState';
import { KYC_LEGAL_NAME_MISSING, KYC_ROUTE, KYC_VERIFICATION_REQUIRED, kycRefusalFor } from '@/utils/kycRefusal';

const lastProps = () => mockEmptyState.mock.calls[mockEmptyState.mock.calls.length - 1][0];

beforeEach(() => jest.clearAllMocks());

it.each([KYC_VERIFICATION_REQUIRED, undefined])('%s: the P2P verify copy and a Verify now action', async (code) => {
  await act(async () => { TestRenderer.create(<KycRequiredState code={code} />); });
  const verify = kycRefusalFor(KYC_VERIFICATION_REQUIRED, undefined, 'p2p')!;
  expect(lastProps()).toMatchObject({ title: verify.title, subtitle: verify.message, actionLabel: 'Verify now' });
  lastProps().onAction();
  expect(mockPush).toHaveBeenCalledWith(KYC_ROUTE);
});

it('legal name missing: the support copy, no Verify action (a dead end)', async () => {
  await act(async () => { TestRenderer.create(<KycRequiredState code={KYC_LEGAL_NAME_MISSING} />); });
  const support = kycRefusalFor(KYC_LEGAL_NAME_MISSING, undefined, 'p2p')!;
  expect(lastProps()).toEqual({ icon: 'info', title: support.title, subtitle: support.message });
});
