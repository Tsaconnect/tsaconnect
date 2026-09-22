/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The address sheet decides what checkout ships to. These guard the flows that
 * change the buyer's pick: saving a new address, editing one, and deleting the
 * last one (which must land on the form, not an empty list).
 */
jest.mock('@react-native-async-storage/async-storage', () => ({
  __esModule: true,
  default: { getItem: jest.fn(async () => null), setItem: jest.fn(async () => null) },
}));

jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
    radius: new Proxy({}, { get: () => 8 }),
  }),
}));

// Host-like stand-ins so the tree can be searched by type and props.
jest.mock('@/components/ui', () => {
  const R = jest.requireActual('react');
  const host = (name: string) => (props: any) => R.createElement(name, props, props.children);
  return {
    __esModule: true,
    Sheet: (props: any) => R.createElement('mock-sheet', { title: props.title }, props.children, props.footer),
    Button: host('mock-button'),
    Icon: () => null,
    IconButton: host('mock-iconbutton'),
    Input: host('mock-input'),
    PressableScale: host('mock-pressable'),
    Text: host('mock-text'),
    Toggle: host('mock-toggle'),
  };
});

jest.mock('@/components/common/LocationPicker', () => {
  const R = jest.requireActual('react');
  return { __esModule: true, default: (props: any) => R.createElement('mock-location', props) };
});

jest.mock('@/services/deliveryAddressApi', () => ({
  __esModule: true,
  ...jest.requireActual('@/services/deliveryAddressApi'),
  createDeliveryAddress: jest.fn(),
  updateDeliveryAddress: jest.fn(),
  deleteDeliveryAddress: jest.fn(),
}));

import React from 'react';
import { Alert } from 'react-native';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import {
  createDeliveryAddress,
  deleteDeliveryAddress,
  updateDeliveryAddress,
  type DeliveryAddress,
} from '@/services/deliveryAddressApi';
import { DeliveryAddressSheet } from '../DeliveryAddressSheet';

const addr = (id: string, isDefault = false): DeliveryAddress => ({
  id,
  isDefault,
  label: id === 'a' ? 'Home' : 'Office',
  recipientName: 'Ada Obi',
  phone: '+2348012345678',
  street: '12 Allen Avenue',
  city: 'Ikeja',
  state: 'Lagos',
  country: 'Nigeria',
  createdAt: '',
  updatedAt: '',
});

const PREFILL = { recipientName: 'Ada Obi', phone: '', country: 'Nigeria', state: 'Lagos', city: 'Ikeja' };

const render = (addresses: DeliveryAddress[], selectedId: string | null, onChanged: jest.Mock) => {
  const onClose = jest.fn();
  let r!: ReactTestRenderer;
  act(() => {
    r = TestRenderer.create(
      <DeliveryAddressSheet
        visible
        onClose={onClose}
        addresses={addresses}
        selectedId={selectedId}
        onSelect={() => {}}
        onChanged={onChanged}
        prefill={PREFILL}
      />,
    );
  });
  return { r, onClose };
};

// The installed test-renderer typings omit `type`; the runtime has it.
const hosts = (r: ReactTestRenderer, kind: string) => r.root.findAll((n) => (n as any).type === kind);
const title = (r: ReactTestRenderer) => hosts(r, 'mock-sheet')[0].props.title;
const input = (r: ReactTestRenderer, label: string) => hosts(r, 'mock-input').find((n) => n.props.label === label)!;
const button = (r: ReactTestRenderer, t: string) => hosts(r, 'mock-button').find((n) => n.props.title === t)!;
const editButtons = (r: ReactTestRenderer) => hosts(r, 'mock-iconbutton');

afterEach(() => jest.clearAllMocks());

it('opens on a prefilled form with no saved address, and refuses an incomplete one', async () => {
  const { r } = render([], null, jest.fn());
  expect(title(r)).toBe('New address');
  expect(input(r, 'Recipient name *').props.value).toBe('Ada Obi');

  await act(async () => {
    await button(r, 'Save address').props.onPress();
  });
  expect(createDeliveryAddress).not.toHaveBeenCalled();
  expect(input(r, 'Phone number *').props.error).toBeDefined();
});

it('saves a new address and makes it the pick', async () => {
  (createDeliveryAddress as jest.Mock).mockResolvedValue({ success: true, data: addr('new', true) });
  const onChanged = jest.fn(async () => [addr('new', true)]);
  const { r, onClose } = render([], null, onChanged);

  act(() => {
    input(r, 'Phone number *').props.onChangeText('+2348012345678');
    input(r, 'Street address *').props.onChangeText('12 Allen Avenue');
  });
  await act(async () => {
    await button(r, 'Save address').props.onPress();
  });

  expect(createDeliveryAddress).toHaveBeenCalledWith(
    expect.objectContaining({ phone: '+2348012345678', street: '12 Allen Avenue', city: 'Ikeja', isDefault: true }),
  );
  expect(onChanged).toHaveBeenCalledWith('new');
  expect(onClose).toHaveBeenCalled();
});

it('keeps the current pick when editing a different address', async () => {
  (updateDeliveryAddress as jest.Mock).mockResolvedValue({ success: true, data: addr('b') });
  const onChanged = jest.fn(async () => [addr('a', true), addr('b')]);
  const { r } = render([addr('a', true), addr('b')], 'a', onChanged);

  const editB = editButtons(r)[1];
  act(() => editB.props.onPress());
  expect(title(r)).toBe('Edit address');
  await act(async () => {
    await button(r, 'Save address').props.onPress();
  });

  expect(updateDeliveryAddress).toHaveBeenCalledWith('b', expect.anything());
  expect(onChanged).toHaveBeenCalledWith(undefined);
});

it('returns to the form after deleting the last address', async () => {
  (deleteDeliveryAddress as jest.Mock).mockResolvedValue({ success: true, data: null });
  const alert = jest.spyOn(Alert, 'alert').mockImplementation(() => {});
  const onChanged = jest.fn(async () => []);
  const { r } = render([addr('a', true)], 'a', onChanged);

  act(() => editButtons(r)[0].props.onPress());
  act(() => button(r, 'Delete address').props.onPress());
  const destructive = alert.mock.calls[0][2]!.find((b) => b.style === 'destructive')!;
  await act(async () => {
    await destructive.onPress!();
  });

  expect(deleteDeliveryAddress).toHaveBeenCalledWith('a');
  expect(title(r)).toBe('New address');
  expect(input(r, 'Street address *').props.value).toBe('');
});
