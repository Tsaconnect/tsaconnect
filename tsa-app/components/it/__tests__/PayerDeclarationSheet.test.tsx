/* eslint-disable import/first -- jest hoists the mocks above the imports they
   replace; writing them first documents which import each one stands in for. */

/**
 * "Paid from another account?": the buyer picks the bank, types the account
 * number and the NAME (never auto-filled — the bank's record has to confirm
 * it), and the server does the rest. A name the bank disagrees with is shown
 * back with the bank's own name.
 */
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: { textPrimary: '#000', textMuted: '#777', danger: '#f00' },
    spacing: { xs: 4, sm: 8, md: 12, lg: 16 },
  }),
}));

jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { Text, View, TextInput, Pressable } = jest.requireActual('react-native');
  return {
    __esModule: true,
    Text: (p: any) => react.createElement(Text, null, p.children),
    Sheet: (p: any) => (p.visible ? react.createElement(View, null, p.children, p.footer) : null),
    Button: (p: any) =>
      react.createElement(Pressable, { testID: `btn:${p.title}`, onPress: p.onPress, disabled: p.disabled },
        react.createElement(Text, null, p.title)),
    SelectRow: (p: any) =>
      react.createElement(Pressable, { testID: 'bank-row', onPress: p.onPress },
        react.createElement(Text, null, p.value ?? p.placeholder)),
    Input: (p: any) =>
      react.createElement(View, null,
        react.createElement(TextInput, { testID: `input:${p.label}`, value: p.value, onChangeText: p.onChangeText }),
        p.error ? react.createElement(Text, null, `error:${p.error}`) : null),
    PickerSheet: (p: any) =>
      p.visible
        ? react.createElement(View, null,
            ...p.items.map((it: any) =>
              react.createElement(Pressable, { key: it.id, testID: `bank:${it.id}`, onPress: () => p.onSelect(it) },
                react.createElement(Text, null, it.label))))
        : null,
  };
});

jest.mock('@/services/ngBanks', () => ({
  __esModule: true,
  getNigerianBanks: jest.fn(async () => [{ code: '999992', name: 'OPay' }]),
}));

jest.mock('@/services/itApi', () => ({ __esModule: true, declarePayer: jest.fn() }));

import React from 'react';
import { Text } from 'react-native';
import { PayerDeclarationSheet } from '../PayerDeclarationSheet';
import { declarePayer } from '@/services/itApi';

// eslint-disable-next-line @typescript-eslint/no-require-imports
const TestRenderer = require('react-test-renderer');
const mockedDeclare = declarePayer as jest.Mock;

async function render(onDeclared = jest.fn()) {
  let tree: any;
  await TestRenderer.act(async () => {
    tree = TestRenderer.create(
      <PayerDeclarationSheet visible orderId="order-1" onClose={jest.fn()} onDeclared={onDeclared} />
    );
  });
  return tree;
}

async function fill(tree: any) {
  await TestRenderer.act(async () => tree.root.findByProps({ testID: 'bank-row' }).props.onPress());
  await TestRenderer.act(async () => tree.root.findByProps({ testID: 'bank:999992' }).props.onPress());
  await TestRenderer.act(async () =>
    tree.root.findByProps({ testID: 'input:Account number' }).props.onChangeText('80-1234-5678'));
  await TestRenderer.act(async () =>
    tree.root.findByProps({ testID: 'input:Account name' }).props.onChangeText('Joyclare Elem'));
}

const allText = (tree: any) => tree.root.findAllByType(Text).map((n: any) => String(n.props.children));

beforeEach(() => mockedDeclare.mockReset());

describe('PayerDeclarationSheet', () => {
  it('cannot be submitted until bank, 10 digits and a name are in', async () => {
    const tree = await render();
    expect(tree.root.findByProps({ testID: 'btn:Confirm account' }).props.disabled).toBe(true);
    await fill(tree);
    expect(tree.root.findByProps({ testID: 'btn:Confirm account' }).props.disabled).toBe(false);
  });

  it('sends the typed details and reports a match', async () => {
    mockedDeclare.mockResolvedValue({
      success: true,
      data: { id: 'd1', orderId: 'order-1', status: 'matched', bankName: 'OPay', accountNumber: '******5678', accountName: 'JOYCLARE ELEM' },
    });
    const onDeclared = jest.fn();
    const tree = await render(onDeclared);
    await fill(tree);
    await TestRenderer.act(async () => tree.root.findByProps({ testID: 'btn:Confirm account' }).props.onPress());

    expect(mockedDeclare).toHaveBeenCalledWith('order-1', '999992', '8012345678', 'Joyclare Elem');
    expect(onDeclared).toHaveBeenCalledWith(expect.objectContaining({ status: 'matched' }));
    expect(allText(tree)).toContain('Payment found and confirmed.');
  });

  it('shows the mismatch under the name, without the bank\'s record', async () => {
    mockedDeclare.mockResolvedValue({
      success: false, code: 'payer_name_mismatch', status: 422,
      message: "That name doesn't match the bank's record for this account. Enter it exactly as your bank app shows it.",
    });
    const onDeclared = jest.fn();
    const tree = await render(onDeclared);
    await fill(tree);
    await TestRenderer.act(async () => tree.root.findByProps({ testID: 'btn:Confirm account' }).props.onPress());

    expect(onDeclared).not.toHaveBeenCalled();
    expect(allText(tree).some((t: string) => t.startsWith("error:That name doesn't match"))).toBe(true);
  });
});
