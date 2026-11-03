/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Foreign payout corridors (2026-11-03): the form renders whatever fields the
 * server's catalog names, loads the country's banks for a `bank` field and the
 * chosen bank's branches for a `branch` field, and never keeps a branch from
 * a bank the user switched away from.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({ spacing: new Proxy({}, { get: () => 8 }) }),
}));

jest.mock('@/components/ui', () => {
  const { View, Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: ({ children }: any) => <RNText>{children}</RNText>,
    Input: (props: any) => <View {...props} />,
    SelectRow: (props: any) => <View {...props} />,
    PickerSheet: (props: any) => <View {...props} />,
  };
});

jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  getBanksForCountry: jest.fn(async () => [
    { code: '11319374', name: 'GHL Bank Ltd', id: '400' },
    { code: '11276282', name: 'First National Bank Ghana', id: '398' },
  ]),
}));

jest.mock('@/services/p2p', () => ({
  __esModule: true,
  banksApi: { branches: jest.fn(async () => ({ success: true, data: [{ code: '18120517JZ', name: 'Head Office' }] })) },
}));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import PayoutDetailsForm, { payoutDetailsComplete } from '@/components/it/PayoutDetailsForm';
import { getBanksForCountry } from '@/services/ngBanks';
import { banksApi } from '@/services/p2p';
import type { PayoutField } from '@/services/itApi';

const GHS_FIELDS: PayoutField[] = [
  { key: 'firstName', label: 'First name', kind: 'text' },
  { key: 'lastName', label: 'Last name', kind: 'text' },
  { key: 'bank', label: 'Bank', kind: 'bank' },
  { key: 'branch', label: 'Branch', kind: 'branch' },
  { key: 'accountNumber', label: 'Account number', kind: 'text' },
];

const host = (tree: ReactTestRenderer, pred: (p: any) => boolean) =>
  tree.root.findAll((n) => typeof (n as any).type === 'string' && pred(n.props));

describe('payoutDetailsComplete', () => {
  it('needs every field filled', () => {
    const full = { firstName: 'Ada', lastName: 'Obi', bank: '1', branch: '2', accountNumber: '3' };
    expect(payoutDetailsComplete(GHS_FIELDS, full)).toBe(true);
    expect(payoutDetailsComplete(GHS_FIELDS, { ...full, branch: ' ' })).toBe(false);
  });
});

describe('PayoutDetailsForm', () => {
  it('loads the country banks and stores the bank id, clearing a stale branch', async () => {
    const onChange = jest.fn();
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(
        <PayoutDetailsForm
          fields={GHS_FIELDS}
          country="GH"
          values={{ branch: 'OLD' }}
          onChange={onChange}
        />,
      );
    });
    expect(getBanksForCountry).toHaveBeenCalledWith('GH');

    await act(async () => host(tree, (p) => p.label === 'Bank')[0].props.onPress());
    await act(async () => host(tree, (p) => typeof p.onSelect === 'function')[0].props.onSelect({ id: '400' }));
    expect(onChange).toHaveBeenLastCalledWith({ branch: '', bank: '11319374', bankId: '400' });
  });

  it('lists the chosen bank branches and blocks the branch until a bank is set', async () => {
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(
        <PayoutDetailsForm fields={GHS_FIELDS} country="GH" values={{}} onChange={jest.fn()} />,
      );
    });
    expect(host(tree, (p) => p.label === 'Branch')[0].props.disabled).toBe(true);
    expect(banksApi.branches).not.toHaveBeenCalled();

    await act(async () => {
      tree.update(
        <PayoutDetailsForm fields={GHS_FIELDS} country="GH" values={{ bank: '11319374', bankId: '400' }} onChange={jest.fn()} />,
      );
    });
    expect(banksApi.branches).toHaveBeenCalledWith('400');
    expect(host(tree, (p) => p.label === 'Branch')[0].props.disabled).toBe(false);
  });

  it('picks the branch when the bank has only one', async () => {
    const onChange = jest.fn();
    await act(async () => {
      TestRenderer.create(
        <PayoutDetailsForm fields={GHS_FIELDS} country="GH" values={{ bank: '11319374', bankId: '400' }} onChange={onChange} />,
      );
    });
    expect(onChange).toHaveBeenLastCalledWith({ bank: '11319374', bankId: '400', branch: '18120517JZ' });
  });

  it('lets the user type a branch code when the bank lists none', async () => {
    (banksApi.branches as jest.Mock).mockResolvedValueOnce({ success: true, data: [] });
    const onChange = jest.fn();
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(
        <PayoutDetailsForm fields={GHS_FIELDS} country="GH" values={{ bank: '11319374', bankId: '401' }} onChange={onChange} />,
      );
    });
    const branch = host(tree, (p) => p.label === 'Branch')[0];
    expect(typeof branch.props.onChangeText).toBe('function');
    expect(branch.props.disabled).toBeUndefined();
    await act(async () => branch.props.onChangeText('GH123'));
    expect(onChange).toHaveBeenLastCalledWith({ bank: '11319374', bankId: '401', branch: 'GH123' });
  });

  it('marks the fields the server refused', async () => {
    let tree!: ReactTestRenderer;
    await act(async () => {
      tree = TestRenderer.create(
        <PayoutDetailsForm fields={GHS_FIELDS} values={{}} onChange={jest.fn()} invalid={['accountNumber']} />,
      );
    });
    expect(host(tree, (p) => p.label === 'Account number')[0].props.error).toBe('Check this field');
    expect(host(tree, (p) => p.label === 'First name')[0].props.error).toBeUndefined();
  });
});
