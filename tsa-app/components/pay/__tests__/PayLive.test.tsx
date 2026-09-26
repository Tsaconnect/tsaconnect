/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * The live-payment UI around usePayNaira / usePayCrypto, from the payer's side.
 * The hooks are stubbed: their own tests pin what happens after a tap. These pin
 * what the SCREEN shows for each state they can be in, and how it keeps the hook
 * in step with the checkout:
 *   - a payment reopened mid-flight still shows its shortfall / failure / Continue
 *     prompts (they used to be hidden with the form);
 *   - a payment the server frees offers Pay again instead of waiting forever;
 *   - a swap still going is reported so the expiry warning cannot tear it down;
 *   - "don't pay again" states never offer Pay.
 */
jest.mock('@react-native-async-storage/async-storage', () =>
  // eslint-disable-next-line @typescript-eslint/no-require-imports -- jest.mock factories cannot import.
  require('@react-native-async-storage/async-storage/jest/async-storage-mock'));
jest.mock('expo-router', () => ({ __esModule: true, router: { push: jest.fn() } }));
jest.mock('@/theme', () => ({
  __esModule: true,
  useTheme: () => ({
    colors: new Proxy({}, { get: () => '#000000' }),
    spacing: new Proxy({}, { get: () => 8 }),
  }),
}));
jest.mock('@/components/ui', () => {
  const react = jest.requireActual('react');
  const { View, Text: RNText } = jest.requireActual('react-native');
  const box = (props: any) => react.createElement(View, null, props.children);
  return {
    __esModule: true,
    Text: (props: any) => react.createElement(RNText, null, props.children),
    Button: (props: any) =>
      react.createElement(View, { accessibilityLabel: `button:${props.title}`, onPress: props.onPress, disabled: !!props.disabled },
        react.createElement(RNText, null, props.title)),
    Spinner: () => null,
    // Each step as `label:state`, so a test can read where the stepper says things are.
    Stepper: (props: any) =>
      react.createElement(View, null, props.steps.map((st: any) => react.createElement(RNText, { key: st.key }, `${st.label}:${st.state}`))),
    Icon: () => null,
    Card: box,
    AccentCard: box,
    PressableScale: (props: any) =>
      react.createElement(View, { accessibilityLabel: 'row:Pay with', onPress: props.onPress }, props.children),
  };
});
const mockAllows: unknown[] = [];
jest.mock('@/components/assets/AssetPicker', () => {
  const react = jest.requireActual('react');
  const { View } = jest.requireActual('react-native');
  return {
    __esModule: true,
    default: (props: any) =>
      (mockAllows.push(props.allow), props.visible ? react.createElement(View, { accessibilityLabel: 'picker', onSelect: props.onSelect }) : null),
  };
});
jest.mock('@/components/paycrest/SwappedSellForm', () => ({
  __esModule: true,
  SWAPPED_STAGE_LABELS: { swapping: 'Swapping your coins…', waiting: 'Waiting for your swap to arrive…' },
}));
jest.mock('@/components/common/AuthorizationModal', () => ({ __esModule: true, default: () => null }));
// The report flow is its own component with its own tests: here, only what it is given.
jest.mock('@/components/disputes/DisputeSection', () => {
  const react = jest.requireActual('react');
  const { Text: RNText } = jest.requireActual('react-native');
  return {
    __esModule: true,
    DisputeSection: (p: any) =>
      react.createElement(RNText, { accessibilityLabel: 'report' }, `report:${p.subjectType}:${p.subjectId}:${p.disputable}`),
  };
});

// The two payment hooks, each a tiny stateful stub the tests steer.
const mockHooks: { naira: any; crypto: any } = { naira: null, crypto: null };
jest.mock('@/hooks/usePayNaira', () => ({ __esModule: true, usePayNaira: () => mockHooks.naira }));
jest.mock('@/hooks/usePayCrypto', () => ({ __esModule: true, usePayCrypto: () => mockHooks.crypto }));

import React from 'react';
import TestRenderer, { act, type ReactTestRenderer } from 'react-test-renderer';
import { router } from 'expo-router';
import type { PayCheckout } from '@/services/payApi';
import { PayLivePanel } from '../PayLivePanel';
import { PayNaira } from '../PayNaira';
import { PayCrypto } from '../PayCrypto';

const ID = '3f2c1a9e-8b7d-4c6e-9f10-1a2b3c4d5e6f';
const fiat = {
  id: ID, merchantName: 'Sky Air', mode: 'live', status: 'pending', customerTotal: 5_050_506, merchantNet: 5_000_000,
  settlementMode: 'fiat', payable: true, youArePaying: false,
} as PayCheckout;
const cryptoCheckout = {
  ...fiat, settlementMode: 'crypto', settleTokenSymbol: 'USDT', settleChainId: 56,
  settleTokenAddress: '0x55d398326f99059fF775485246999027B3197955',
} as PayCheckout;
const bnb = { address: '0x0', symbol: 'BNB', decimals: 18 };

const hookState = (over: Record<string, unknown> = {}) => ({
  stage: 'idle', retryable: true, canContinue: false, walletShort: false, walletAddress: '0xPayer',
  payWith: jest.fn(), estimate: jest.fn(async () => ({ pay: '0.084 BNB', fromAmount: '84000000000000000' })),
  completeWithWallet: jest.fn(), continuePayment: jest.fn(), resume: jest.fn(async () => {}), reset: jest.fn(),
  authorization: { visible: false, title: '', onAuthorized: jest.fn(), onCancel: jest.fn() },
  ...over,
});

function textNodes(node: any, out: string[] = []): string[] {
  if (node == null) return out;
  if (typeof node === 'string') { out.push(node); return out; }
  if (Array.isArray(node)) { node.forEach((n) => textNodes(n, out)); return out; }
  (node.children ?? []).forEach((c: any) => textNodes(c, out));
  return out;
}
const text = (t: ReactTestRenderer) => textNodes(t.toJSON()).join(' ');
const find = (t: ReactTestRenderer, label: string) => t.root.findAll((n) => n.props.accessibilityLabel === label)[0];
const flush = async () => { for (let i = 0; i < 6; i += 1) await act(async () => { await Promise.resolve(); }); };

let tree: ReactTestRenderer;
afterEach(() => { act(() => tree?.unmount()); jest.clearAllMocks(); });

const panel = (pay: any, props: Partial<React.ComponentProps<typeof PayLivePanel>> = {}) =>
  act(() => { tree = TestRenderer.create(<PayLivePanel checkout={fiat} pay={pay} showForm {...props} />); });

/** Pick BNB in the panel, so it has something priced. */
async function pick() {
  await act(async () => { find(tree, 'row:Pay with').props.onPress(); });
  await act(async () => { find(tree, 'picker').props.onSelect(bnb, 56); });
  await flush();
}

describe('the panel, by state', () => {
  it('idle with the form: the picker row and a Pay button that waits for a priced pick', async () => {
    await panel(hookState());
    expect(text(tree)).toContain('Choose a token');
    expect(find(tree, 'button:Pay ₦50,505.06').props.disabled).toBe(true);
    await pick();
    expect(text(tree)).toContain("You'll pay about 0.084 BNB");
    expect(find(tree, 'button:Pay ₦50,505.06').props.disabled).toBe(false);
  });

  it('a shortfall shows the question and Use my USDC — even when the form is hidden (a reopened payment)', async () => {
    const pay = hookState({ stage: 'shortfall', missingUsdc: '0.42' });
    await panel(pay, { showForm: false });
    expect(text(tree)).toContain('about 0.42 USDC short');
    await act(async () => { find(tree, 'button:Use my USDC').props.onPress(); });
    expect(pay.completeWithWallet).toHaveBeenCalled();
    // No form: no picker row, no Pay button.
    expect(find(tree, 'row:Pay with')).toBeUndefined();
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
  });

  it('a second shortfall says her wallet was not enough', async () => {
    await panel(hookState({ stage: 'shortfall', missingUsdc: '0.10', walletShort: true }), { showForm: false });
    expect(text(tree)).toContain("isn't enough to cover the rest");
    expect(text(tree)).not.toContain('The rate moved');
  });

  it('a failure shows its words with the form hidden', async () => {
    await panel(hookState({ stage: 'failed', error: 'The swap did not go through' }), { showForm: false });
    expect(text(tree)).toContain('The swap did not go through');
  });

  it('a KYC refusal offers Verify identity', async () => {
    await panel(hookState({ stage: 'failed', error: 'Verify first', errorCode: 'kyc_verification_required' }));
    await act(async () => { find(tree, 'button:Verify identity').props.onPress(); });
    expect(router.push).toHaveBeenCalledWith('/profile/kyc');
  });

  it('a payment under way offers Continue payment, and the form stays hidden', async () => {
    const pay = hookState({ stage: 'continue', canContinue: true });
    await panel(pay, { showForm: false });
    await act(async () => { find(tree, 'button:Continue payment').props.onPress(); });
    expect(pay.continuePayment).toHaveBeenCalled();
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
  });

  it('progress shows while paying', async () => {
    await panel(hookState({ stage: 'paying', swapStage: 'swapping' }), { showForm: false });
    expect(text(tree)).toContain('Swapping your coins…');
    expect(text(tree)).toContain('Keep the app open');
  });

  it('"don\'t pay again" states offer Continue payment and never Pay', async () => {
    const pay = hookState({ stage: 'failed', error: "don't pay again", retryable: false, canContinue: true });
    await panel(pay);
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
    expect(find(tree, 'button:Continue payment')).toBeDefined();
  });

  it('a retryable failure still offers Pay', async () => {
    await panel(hookState({ stage: 'failed', error: 'User rejected the request' }));
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeDefined();
  });

  it('the panel owns "waiting" for a live payment, with the form or without (the screen shows its own only for test payments)', async () => {
    await panel(hookState({ stage: 'waiting' }), { showForm: false });
    expect(text(tree).match(/Waiting for confirmation/g)).toHaveLength(1);
    act(() => tree.unmount());
    await panel(hookState({ stage: 'waiting' }), { showForm: true });
    expect(text(tree).match(/Waiting for confirmation/g)).toHaveLength(1);
  });

  it('a reopened payment the hook has not read yet says it is checking — never "waiting" for something unknown', async () => {
    await panel(hookState({ stage: 'idle' }), { showForm: false });
    expect(text(tree)).toContain('Checking on your payment');
    expect(text(tree)).not.toContain('Waiting for confirmation');
  });

  it('with no wallet on this device that can sign, it says so instead of checking forever', async () => {
    await panel(hookState({ stage: 'idle', walletAddress: null, walletUnavailable: true }), { showForm: false });
    expect(text(tree)).toContain('no wallet that can sign for this payment');
    expect(text(tree)).not.toContain('Checking on your payment');
  });

  it('a wait with a notice (a send whose outcome is unknown) shows it under the spinner — and offers no Send', async () => {
    await panel(hookState({ stage: 'waiting', notice: "We couldn't confirm whether your payment was sent." }), { showForm: false });
    expect(text(tree)).toContain('Waiting for confirmation');
    expect(text(tree)).toContain("We couldn't confirm whether your payment was sent.");
    expect(text(tree)).not.toMatch(/Send /);
  });
});

describe('a direct order whose deposit never left her wallet', () => {
  const order = {
    id: 'o1', chain: 'base', token: 'USDC', tokenAmount: '0.742116', status: 'awaiting_deposit',
    validUntil: new Date(Date.now() + 42 * 60_000 + 30_000).toISOString(),
  };

  it('says what is left to send, why the last try failed, and offers Send for THAT amount — not Pay, not Waiting', async () => {
    const sendDeposit = jest.fn();
    await panel(hookState({ stage: 'deposit', retryable: false, order, error: 'Covering the network fee is temporarily unavailable.', sendDeposit }), { showForm: false });
    const t = text(tree);
    expect(t).toContain('Finish sending your payment');
    expect(t).toContain('0.742116 USDC on Base still needs to leave your wallet');
    expect(t).toContain('Covering the network fee is temporarily unavailable.');
    expect(t).toContain('none of your money is taken. Once the order closes in about 42 minutes');
    expect(find(tree, 'button:Send 0.742116 USDC')).toBeDefined();
    expect(t).not.toContain('Waiting for confirmation');
    expect(find(tree, 'button:Continue payment')).toBeUndefined();

    act(() => find(tree, 'button:Send 0.742116 USDC').props.onPress());
    expect(sendDeposit).toHaveBeenCalledTimes(1);
  });

  it('never offers Pay — even with the form up, where Pay could otherwise show: the order exists', async () => {
    await panel(hookState({ stage: 'deposit', retryable: false, order, sendDeposit: jest.fn() }), { showForm: true });
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
    expect(find(tree, 'button:Send 0.742116 USDC')).toBeDefined();
  });

  it('does not link the order screen, whose Cancel would fail the payment for good', async () => {
    await panel(hookState({ stage: 'deposit', retryable: false, order, sendDeposit: jest.fn() }), {
      showForm: false, checkout: { ...fiat, orderId: 'o1' } as PayCheckout,
    });
    expect(text(tree)).not.toContain('report:');
    expect(find(tree, 'button:Open the payment order')).toBeUndefined();
  });

  it('with no reason from a failed send it shows none', async () => {
    await panel(hookState({ stage: 'deposit', retryable: false, order, sendDeposit: jest.fn() }), { showForm: false });
    expect(text(tree)).not.toMatch(/unavailable|failed|could not/i);
  });
});

describe('reporting a problem with the payment', () => {
  it('a failed attempt whose money moved offers Report a problem on the payment — never the order screen', async () => {
    await panel(hookState({ stage: 'failed', error: 'Could not send', retryable: false, order: { id: 'o1', status: 'pending' } }));
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
    expect(text(tree)).toContain('report:paycrest_order:o1:true');
    expect(find(tree, 'button:Open the payment order')).toBeUndefined();
    expect(router.push).not.toHaveBeenCalled();
  });

  it('a reopened payment reports against the order the server linked, with its status from the checkout', async () => {
    await panel(hookState({ stage: 'waiting' }), {
      showForm: false, checkout: { ...fiat, orderId: 'o9', orderStatus: 'settling' } as PayCheckout,
    });
    expect(text(tree)).toContain('report:paycrest_order:o9:true');
  });

  it('before any money has moved there is nothing to report — the flow is told so', async () => {
    await panel(hookState({ stage: 'waiting' }), {
      showForm: false, checkout: { ...fiat, orderId: 'o9', orderStatus: 'awaiting_deposit' } as PayCheckout,
    });
    expect(text(tree)).toContain('report:paycrest_order:o9:false');
  });

  it('without an order, or its status, it is not offered', async () => {
    await panel(hookState({ stage: 'waiting' }), { showForm: false });
    expect(text(tree)).not.toContain('report:');
    act(() => tree.unmount());
    await panel(hookState({ stage: 'waiting' }), { showForm: false, checkout: { ...fiat, orderId: 'o9' } as PayCheckout });
    expect(text(tree)).not.toContain('report:');
  });

  it('a linked order hides Pay: an attempt exists', async () => {
    await panel(hookState({ stage: 'idle' }), { checkout: { ...fiat, orderId: 'o9' } as PayCheckout });
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();
  });
});

describe('the price line', () => {
  it('shows why when the price cannot be worked out — never a silently disabled Pay', async () => {
    await panel(hookState({ estimate: jest.fn(async () => { throw new Error('Connect a wallet on this device first.'); }) }));
    await pick();
    expect(text(tree)).toContain('Connect a wallet on this device first.');
  });

  it('says so when the estimate comes back empty', async () => {
    await panel(hookState({ estimate: jest.fn(async () => ({ pay: '', fromAmount: '' })) }));
    await pick();
    expect(text(tree)).toContain("couldn't work out the price");
    expect(find(tree, 'button:Pay ₦50,505.06').props.disabled).toBe(true);
  });

  it('a crypto merchant: what the store receives, with the amount when the server gave one', async () => {
    const receives = (amount?: string) => `The store receives ${amount ?? 'USDT'} on BNB Smart Chain.`;
    await panel(
      hookState({ estimate: jest.fn(async () => ({ pay: '0.084 BNB', fromAmount: '84000000000000000', receives: '33.32 USDT' })) }),
      { checkout: cryptoCheckout, receives },
    );
    expect(text(tree)).toContain('The store receives USDT on BNB Smart Chain.');
    await pick();
    expect(text(tree)).toContain('The store receives 33.32 USDT on BNB Smart Chain.');
  });
});

describe('PayNaira / PayCrypto keep the hook in step with the checkout', () => {
  const naira = (props: Partial<React.ComponentProps<typeof PayNaira>> = {}) => (
    <PayNaira checkout={{ ...fiat, status: 'processing', youArePaying: true } as PayCheckout} showForm={false}
      onWaiting={jest.fn()} onActive={jest.fn()} {...props} />
  );

  it('opening on a payment of hers already under way follows it once, and shows what it finds', async () => {
    mockHooks.naira = hookState({ stage: 'idle' });
    await act(async () => { tree = TestRenderer.create(naira()); });
    await flush();
    expect(mockHooks.naira.resume).toHaveBeenCalledTimes(1);

    // The follow lands in a shortfall: the prompt must be there, not just "Waiting…".
    mockHooks.naira = hookState({ stage: 'shortfall', missingUsdc: '0.42' });
    await act(async () => { tree.update(naira()); });
    expect(text(tree)).toContain('Use my USDC');
    expect(mockHooks.naira.resume).not.toHaveBeenCalled(); // once only
  });

  it('while that follow is still reading, both wrappers say they are checking — never a blank panel', async () => {
    mockHooks.naira = hookState({ stage: 'idle' });
    await act(async () => { tree = TestRenderer.create(naira()); });
    expect(text(tree)).toContain('Checking on your payment');
    act(() => tree.unmount());

    mockHooks.crypto = hookState({ stage: 'idle' });
    await act(async () => {
      tree = TestRenderer.create(
        <PayCrypto checkout={{ ...cryptoCheckout, status: 'processing', youArePaying: true } as PayCheckout} showForm={false}
          onWaiting={jest.fn()} onActive={jest.fn()} />,
      );
    });
    expect(text(tree)).toContain('Checking on your payment');
  });

  it('a failure found on resume is shown too', async () => {
    mockHooks.naira = hookState({ stage: 'failed', error: 'We could not confirm it', retryable: false, canContinue: true });
    await act(async () => { tree = TestRenderer.create(naira()); });
    expect(text(tree)).toContain('We could not confirm it');
  });

  it('a payer with the form up does not trigger a follow', async () => {
    mockHooks.naira = hookState({ stage: 'idle' });
    await act(async () => { tree = TestRenderer.create(naira({ showForm: true, checkout: fiat })); });
    await flush();
    expect(mockHooks.naira.resume).not.toHaveBeenCalled();
  });

  it('when the server frees the payment the hook is reset, so Pay is offered again', async () => {
    const state = { stage: 'waiting' as string };
    mockHooks.naira = hookState({ stage: 'waiting', reset: jest.fn(() => { state.stage = 'idle'; }) });
    await act(async () => { tree = TestRenderer.create(naira()); });
    const reset = mockHooks.naira.reset;
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeUndefined();

    // The reaper released it: processing → pending, payable again.
    mockHooks.naira = hookState({ stage: 'waiting', reset });
    await act(async () => { tree.update(naira({ checkout: fiat, showForm: true })); });
    expect(reset).toHaveBeenCalledTimes(1);
    mockHooks.naira = hookState({ stage: 'idle', reset });
    await act(async () => { tree.update(naira({ checkout: fiat, showForm: true })); });
    expect(find(tree, 'button:Pay ₦50,505.06')).toBeDefined();
  });

  it('a hook stuck waiting is reset by a refetch that shows the payment pending — no processing edge needed', async () => {
    // e.g. no sale of hers belongs to this payment, so the follow only waited.
    mockHooks.naira = hookState({ stage: 'waiting' });
    await act(async () => { tree = TestRenderer.create(naira({ checkout: fiat, showForm: true })); });
    const reset = mockHooks.naira.reset;
    expect(reset).not.toHaveBeenCalled(); // mounting is not a refetch

    await act(async () => { tree.update(naira({ checkout: { ...fiat } as PayCheckout, showForm: true })); });
    expect(reset).toHaveBeenCalledTimes(1);
  });

  it('but a refetch that still shows her payment processing leaves a waiting hook alone', async () => {
    const processing = { ...fiat, status: 'processing', youArePaying: true } as PayCheckout;
    mockHooks.naira = hookState({ stage: 'waiting' });
    await act(async () => { tree = TestRenderer.create(naira({ checkout: processing })); });
    await act(async () => { tree.update(naira({ checkout: { ...processing } as PayCheckout })); });
    expect(mockHooks.naira.reset).not.toHaveBeenCalled();
  });

  it('a stage that is not waiting is not reset by a plain pending refetch — only by the release edge', async () => {
    mockHooks.naira = hookState({ stage: 'failed', error: 'x', retryable: false });
    await act(async () => { tree = TestRenderer.create(naira({ checkout: fiat, showForm: true })); });
    await act(async () => { tree.update(naira({ checkout: { ...fiat } as PayCheckout, showForm: true })); });
    expect(mockHooks.naira.reset).not.toHaveBeenCalled();
  });

  it('reports an attempt that is moving money, so the expiry warning cannot tear the form down', async () => {
    const onActive = jest.fn();
    mockHooks.naira = hookState({ stage: 'paying' });
    await act(async () => { tree = TestRenderer.create(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(true);

    mockHooks.naira = hookState({ stage: 'shortfall' });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(true);

    // A failure — even one that says don't pay again — and a wait on the server are
    // still an unresolved attempt: the form and its safety copy must outlive the warning.
    mockHooks.naira = hookState({ stage: 'failed', error: 'x', retryable: false });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(true);

    mockHooks.naira = hookState({ stage: 'waiting' });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(true);

    // A failure that is safe to retry (nothing was started, or it was closed) is not.
    mockHooks.naira = hookState({ stage: 'failed', error: 'x', retryable: true });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(false);

    // Only reset() (back to idle) ends it.
    mockHooks.naira = hookState({ stage: 'idle' });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(false);

    mockHooks.naira = hookState({ stage: 'continue' });
    await act(async () => { tree.update(naira({ onActive, showForm: true, checkout: fiat })); });
    expect(onActive).toHaveBeenLastCalledWith(true);
    act(() => tree.unmount());
    expect(onActive).toHaveBeenLastCalledWith(false);
  });

  it('refreshes the checkout when the attempt hands over to the server', async () => {
    const onWaiting = jest.fn();
    mockHooks.naira = hookState({ stage: 'waiting' });
    await act(async () => { tree = TestRenderer.create(naira({ onWaiting })); });
    expect(onWaiting).toHaveBeenCalled();
  });

  it('PayCrypto: a failure on a reopened payment is shown, with the store’s token named', async () => {
    mockHooks.crypto = hookState({ stage: 'failed', error: "Don't pay again", retryable: false, canContinue: true });
    await act(async () => {
      tree = TestRenderer.create(
        <PayCrypto checkout={{ ...cryptoCheckout, status: 'processing', youArePaying: true } as PayCheckout}
          showForm={false} onWaiting={jest.fn()} onActive={jest.fn()} />,
      );
    });
    expect(text(tree)).toContain("Don't pay again");
    expect(find(tree, 'button:Continue payment')).toBeDefined();
    expect(mockHooks.crypto.resume).toHaveBeenCalledTimes(0); // stage is not idle: nothing to follow
  });

  it('PayCrypto: the form names what the store receives and the token that is coming soon', async () => {
    mockHooks.crypto = hookState({ stage: 'idle' });
    await act(async () => {
      tree = TestRenderer.create(
        <PayCrypto checkout={cryptoCheckout} showForm onWaiting={jest.fn()} onActive={jest.fn()} />,
      );
    });
    expect(text(tree)).toContain('The store receives USDT on BNB Smart Chain.');
    expect(text(tree)).toContain('Paying with USDT on BNB Smart Chain directly is coming soon');
  });
});

describe('the price she is shown is the price she pays', () => {
  beforeEach(() => { jest.useFakeTimers({ doNotFake: ['nextTick', 'setImmediate', 'queueMicrotask'] }); });
  afterEach(() => { jest.useRealTimers(); });
  const settleTimers = async (ms: number) => { await act(async () => { jest.advanceTimersByTime(ms); }); await flush(); };

  it('Pay hands the hook the very figure she saw, base units and all', async () => {
    const pay = hookState();
    await panel(pay);
    await pick();
    await act(async () => { find(tree, 'button:Pay ₦50,505.06').props.onPress(); });
    expect(pay.payWith).toHaveBeenCalledWith(
      { ...bnb, chainId: 56 }, 56, { pay: '0.084 BNB', fromAmount: '84000000000000000' },
    );
  });

  it('re-prices on its own when the figure is 30 seconds old, and shows the new one', async () => {
    const estimate = jest.fn()
      .mockResolvedValueOnce({ pay: '0.084 BNB', fromAmount: '84000000000000000' })
      .mockResolvedValue({ pay: '0.09 BNB', fromAmount: '90000000000000000' });
    await panel(hookState({ estimate }));
    await pick();
    expect(text(tree)).toContain("You'll pay about 0.084 BNB");
    await settleTimers(31_000);
    expect(estimate).toHaveBeenCalledTimes(2);
    expect(text(tree)).toContain("You'll pay about 0.09 BNB");
  });

  it('does not keep re-pricing while a payment is being made', async () => {
    const estimate = jest.fn(async () => ({ pay: '0.084 BNB', fromAmount: '84000000000000000' }));
    const pay = hookState({ estimate });
    await panel(pay);
    await pick();
    await act(async () => { tree.update(<PayLivePanel checkout={fiat} pay={{ ...pay, stage: 'paying' } as any} showForm />); });
    await settleTimers(61_000);
    expect(estimate).toHaveBeenCalledTimes(1);
  });

  it('a Pay tap on a stale figure re-prices instead of paying it: she confirms what it is now', async () => {
    const pay = hookState();
    await panel(pay);
    await pick();
    // Hold the timer refresh off and let the figure go stale.
    const realNow = Date.now();
    const spy = jest.spyOn(Date, 'now').mockReturnValue(realNow + 45_000);
    pay.estimate.mockResolvedValue({ pay: '0.09 BNB', fromAmount: '90000000000000000' });
    await act(async () => { find(tree, 'button:Pay ₦50,505.06').props.onPress(); });
    await flush();
    expect(pay.payWith).not.toHaveBeenCalled();
    expect(text(tree)).toContain("You'll pay about 0.09 BNB");

    await act(async () => { find(tree, 'button:Pay ₦50,505.06').props.onPress(); });
    expect(pay.payWith).toHaveBeenCalledWith(expect.anything(), 56, { pay: '0.09 BNB', fromAmount: '90000000000000000' });
    spy.mockRestore();
  });

  it('when a refusal comes back (the stage flips to failed) the figure is looked up again at once — the next tap must not be on a stale price', async () => {
    const estimate = jest.fn()
      .mockResolvedValueOnce({ pay: '0.084 BNB', fromAmount: '84000000000000000' })
      .mockResolvedValue({ pay: '0.09 BNB', fromAmount: '90000000000000000' });
    const pay = hookState({ estimate });
    await panel(pay);
    await pick();
    expect(estimate).toHaveBeenCalledTimes(1);

    await act(async () => {
      tree.update(<PayLivePanel checkout={fiat} pay={{ ...pay, stage: 'failed', error: 'The price moved' } as any} showForm />);
    });
    await flush();
    expect(estimate).toHaveBeenCalledTimes(2);
    expect(text(tree)).toContain("You'll pay about 0.09 BNB");
  });

  it('a figure with no base-unit amount cannot be paid: the button is disabled and it says why', async () => {
    const pay = hookState({ estimate: jest.fn(async () => ({ pay: '0.084 BNB', fromAmount: '' })) });
    await panel(pay);
    await pick();
    expect(find(tree, 'button:Pay ₦50,505.06').props.disabled).toBe(true);
    expect(text(tree)).toContain("couldn't work out the price");
    expect(text(tree)).not.toContain("You'll pay about");
  });

  it('a wallet that arrives after the pick is priced when it arrives — the "Connect a wallet" wait is not final', async () => {
    const estimate = jest.fn()
      .mockRejectedValueOnce(new Error('Connect a wallet on this device first — you pay from it yourself.'))
      .mockResolvedValue({ pay: '0.084 BNB', fromAmount: '84000000000000000' });
    const pay = hookState({ estimate, walletAddress: null });
    await panel(pay);
    await pick();
    expect(text(tree)).toContain('Connect a wallet');

    await act(async () => { tree.update(<PayLivePanel checkout={fiat} pay={{ ...pay, walletAddress: '0xPayer' } as any} showForm />); });
    await flush();
    expect(text(tree)).toContain("You'll pay about 0.084 BNB");
  });
});

describe('the picker is not rebuilt every time the screen re-renders', () => {
  it('PayNaira hands the picker the same `allow` across renders of an unchanged merchant', async () => {
    mockHooks.naira = hookState({ stage: 'idle' });
    mockAllows.length = 0;
    const c1 = { ...fiat } as PayCheckout;
    await act(async () => { tree = TestRenderer.create(<PayNaira checkout={c1} showForm onWaiting={jest.fn()} onActive={jest.fn()} />); });
    await act(async () => { tree.update(<PayNaira checkout={{ ...fiat } as PayCheckout} showForm onWaiting={jest.fn()} onActive={jest.fn()} />); });
    const seen = mockAllows.filter(Boolean);
    expect(seen.length).toBeGreaterThan(1);
    expect(new Set(seen).size).toBe(1);
  });

  it('but a different merchant token gets a new one', async () => {
    mockHooks.crypto = hookState({ stage: 'idle' });
    mockAllows.length = 0;
    await act(async () => { tree = TestRenderer.create(<PayCrypto checkout={cryptoCheckout} showForm onWaiting={jest.fn()} onActive={jest.fn()} />); });
    await act(async () => {
      tree.update(<PayCrypto checkout={{ ...cryptoCheckout, settleChainId: 8453 } as PayCheckout} showForm onWaiting={jest.fn()} onActive={jest.fn()} />);
    });
    expect(new Set(mockAllows.filter(Boolean)).size).toBe(2);
  });
});

describe('a naira payment shows where it has got to', () => {
  const order = { id: 'o1', status: 'awaiting_deposit' };
  const mount = async (pay: any, over: Partial<PayCheckout> = {}) => {
    mockHooks.naira = pay;
    await act(async () => {
      tree = TestRenderer.create(<PayNaira checkout={{ ...fiat, ...over }} showForm={false} onWaiting={jest.fn()} onActive={jest.fn()} />);
    });
  };
  const rerender = async (pay: any, over: Partial<PayCheckout> = {}) => {
    mockHooks.naira = pay;
    await act(async () => {
      tree.update(<PayNaira checkout={{ ...fiat, ...over }} showForm={false} onWaiting={jest.fn()} onActive={jest.fn()} />);
    });
  };

  it('steps replace the one-line progress while a swap runs, and the keep-open advice stays', async () => {
    await mount(hookState({ stage: 'paying', swapStage: 'swapping', swapped: true }));
    expect(text(tree)).toContain('Swap to USDC:active');
    expect(text(tree)).toContain('Paying Sky Air:pending');
    expect(text(tree)).not.toContain('Swapping your coins…');
    expect(text(tree)).toContain('Keep the app open');
  });

  it('just after Pay, before the attempt has a shape, it says it is starting — no steps yet', async () => {
    await mount(hookState({ stage: 'paying', swapStage: 'idle' }));
    expect(text(tree)).toContain('Starting your payment…');
    expect(text(tree)).not.toMatch(/:(done|active|pending)/);
  });

  it('a direct payment with its order is sending from her wallet', async () => {
    await mount(hookState({ stage: 'paying', swapStage: 'idle', order }));
    expect(text(tree)).toContain('Payment sent:active');
    expect(text(tree)).not.toContain('Swap to USDC');
  });

  it("while it waits, the checkout's order status moves the steps — and 'Waiting for confirmation' gives way to them", async () => {
    await mount(hookState({ stage: 'waiting', order }), { orderId: 'o1' });
    expect(text(tree)).toContain('Received by the provider:active');
    expect(text(tree)).not.toContain('Waiting for confirmation');
    await rerender(hookState({ stage: 'waiting', order }), { orderId: 'o1', orderStatus: 'settling' });
    expect(text(tree)).toContain('Received by the provider:done');
    expect(text(tree)).toContain('Paying Sky Air:active');
  });

  it('a send whose outcome is unknown is checked, never shown sent — beside its notice', async () => {
    const notice = "We couldn't confirm whether your payment was sent.";
    await mount(hookState({ stage: 'waiting', order, notice }), { orderId: 'o1' });
    expect(text(tree)).toContain('Payment sent:active');
    expect(text(tree)).not.toContain('Payment sent:done');
    expect(text(tree)).toContain(notice);
  });

  it('a wait with no order shows no steps, just the wait line', async () => {
    await mount(hookState({ stage: 'waiting' }));
    expect(text(tree)).not.toMatch(/:(done|active|pending)/);
    expect(text(tree)).toContain('Waiting for confirmation');
  });

  it('an order that ended badly shows no steps, just the wait line', async () => {
    await mount(hookState({ stage: 'waiting', order }), { orderId: 'o1', orderStatus: 'expired' });
    expect(text(tree)).not.toMatch(/:(done|active|pending)/);
    expect(text(tree)).toContain('Waiting for confirmation');
  });
});
