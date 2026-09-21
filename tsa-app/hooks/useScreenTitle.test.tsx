/* eslint-disable import/first -- jest hoists mocks above imports */
import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';

const mockSetOptions = jest.fn();
jest.mock('expo-router', () => ({
  useNavigation: () => ({ setOptions: mockSetOptions }),
}));

import { useScreenTitle } from './useScreenTitle';

function Probe({ title }: { title: string }) {
  useScreenTitle(title);
  return null;
}

describe('useScreenTitle', () => {
  beforeEach(() => mockSetOptions.mockClear());

  it('sets the title on mount and again when it changes', async () => {
    let renderer: ReactTestRenderer | undefined;
    await act(async () => {
      renderer = TestRenderer.create(<Probe title="Sell for Naira" />);
    });
    expect(mockSetOptions).toHaveBeenLastCalledWith({ title: 'Sell for Naira' });

    await act(async () => {
      renderer?.update(<Probe title="Sell for UGX" />);
    });
    expect(mockSetOptions).toHaveBeenLastCalledWith({ title: 'Sell for UGX' });
    expect(mockSetOptions).toHaveBeenCalledTimes(2);

    await act(async () => {
      renderer?.unmount();
    });
  });
});
