/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
/**
 * Didit's hosted-flow callback (tsaconnect://kyc/callback): a name change's
 * session comes back with ?flow=name_change and belongs to the name-change
 * screen; everything else is first-time KYC, routed exactly as before.
 */
const mockParams: { value: Record<string, string | undefined> } = { value: {} };
jest.mock('expo-router', () => {
  const react = jest.requireActual('react');
  const { Text } = jest.requireActual('react-native');
  return {
    __esModule: true,
    useLocalSearchParams: () => mockParams.value,
    Redirect: (props: { href: string }) => react.createElement(Text, null, `redirect:${props.href}`),
  };
});

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import KYCCallback from '../callback';

async function landing(params: Record<string, string | undefined>): Promise<string> {
  mockParams.value = params;
  let tree!: ReactTestRenderer;
  await act(async () => { tree = TestRenderer.create(<KYCCallback />); });
  return JSON.stringify(tree.toJSON());
}

it('a name change\'s session returns to the name-change screen', async () => {
  await expect(landing({ flow: 'name_change', verificationSessionId: 's1', status: 'Approved' }))
    .resolves.toContain('redirect:/profile/name-change');
});

it('first-time KYC is unchanged: with or without Didit\'s own params, or any other flow', async () => {
  await expect(landing({})).resolves.toContain('redirect:/profile/kyc');
  await expect(landing({ verificationSessionId: 's1', status: 'Declined' })).resolves.toContain('redirect:/profile/kyc');
  await expect(landing({ flow: 'something_else' })).resolves.toContain('redirect:/profile/kyc');
});
