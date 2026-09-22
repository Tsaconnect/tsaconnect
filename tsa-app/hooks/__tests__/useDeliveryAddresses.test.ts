/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/services/deliveryAddressApi', () => ({ __esModule: true, listDeliveryAddresses: jest.fn() }));

import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';
import { listDeliveryAddresses, type DeliveryAddress } from '@/services/deliveryAddressApi';
import { pickAddress, useDeliveryAddresses, validateAddressForm } from '../useDeliveryAddresses';

const mockList = listDeliveryAddresses as jest.Mock;

const addr = (id: string, isDefault = false): DeliveryAddress => ({
  id,
  isDefault,
  recipientName: 'Ada',
  phone: '1',
  street: 's',
  city: 'Ikeja',
  state: 'Lagos',
  country: 'Nigeria',
  createdAt: '',
  updatedAt: '',
});

describe('pickAddress', () => {
  it('keeps the current pick while it still exists', () => {
    expect(pickAddress([addr('a', true), addr('b')], 'b')).toBe('b');
  });

  it('falls back to the default when the pick was deleted', () => {
    expect(pickAddress([addr('a'), addr('b', true)], 'gone')).toBe('b');
  });

  it('falls back to the first address when none is default', () => {
    expect(pickAddress([addr('a'), addr('b')], null)).toBe('a');
  });

  it('is null with no addresses', () => {
    expect(pickAddress([], 'a')).toBeNull();
  });
});

describe('validateAddressForm', () => {
  const valid = { recipientName: 'Ada', phone: '+234 801 234 5678', street: '12 Allen Ave', city: 'Ikeja', state: 'Lagos', country: 'Nigeria' };

  it('accepts a complete address', () => {
    expect(validateAddressForm(valid)).toEqual({});
  });

  it('flags every blank required field', () => {
    const errors = validateAddressForm({ recipientName: ' ', phone: '', street: '', city: '', state: '', country: '' });
    expect(Object.keys(errors).sort()).toEqual(['city', 'country', 'phone', 'recipientName', 'state', 'street']);
  });

  it('rejects a phone number without enough digits', () => {
    expect(validateAddressForm({ ...valid, phone: '12-34' }).phone).toBeDefined();
  });
});

describe('useDeliveryAddresses', () => {
  const mount = async () => {
    const hook: { current: ReturnType<typeof useDeliveryAddresses> | null } = { current: null };
    const Probe = () => {
      hook.current = useDeliveryAddresses();
      return null;
    };
    await act(async () => {
      TestRenderer.create(React.createElement(Probe));
    });
    return hook as { current: ReturnType<typeof useDeliveryAddresses> };
  };

  afterEach(() => mockList.mockReset());

  it('reports a failed load as an error, not an empty address book', async () => {
    mockList.mockResolvedValue({ success: false, message: 'offline' });
    const hook = await mount();

    expect(hook.current.loading).toBe(false);
    expect(hook.current.error).toBe('offline');
  });

  it('keeps the previous list and pick when a reload fails', async () => {
    mockList
      .mockResolvedValueOnce({ success: true, data: [addr('a', true)] })
      .mockResolvedValueOnce({ success: false, message: 'offline' });
    const hook = await mount();
    expect(hook.current.selected?.id).toBe('a');

    let reloaded: unknown;
    await act(async () => {
      reloaded = await hook.current.reload();
    });
    expect(reloaded).toBeNull();
    expect(hook.current.addresses).toHaveLength(1);
    expect(hook.current.selected?.id).toBe('a');
    expect(hook.current.error).toBe('offline');
  });

  it('selects a just-saved address on reload', async () => {
    mockList
      .mockResolvedValueOnce({ success: true, data: [addr('a', true)] })
      .mockResolvedValueOnce({ success: true, data: [addr('a', true), addr('b')] });
    const hook = await mount();

    await act(async () => {
      await hook.current.reload('b');
    });
    expect(hook.current.selected?.id).toBe('b');
    expect(hook.current.error).toBeNull();
  });
});
