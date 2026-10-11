// services/__tests__/publishFailureFlow.test.ts — escrow funding → the
// "Could not publish ad" body, end to end. The prepare refusal is thrown as
// mapped copy; publishFailureMessage must still decode the ERC20 revert hex the
// backend's gas estimate carries (review I1).
const mockPool = {
  balance: jest.fn(),
  allowance: jest.fn(),
  prepareApprove: jest.fn(),
  prepareDeposit: jest.fn(),
  confirmDeposit: jest.fn(),
};
jest.mock('../p2p', () => ({
  ...jest.requireActual('../p2p'),
  // Wrappers: the factory runs before the const above is initialised.
  poolApi: {
    balance: (...a: any[]) => mockPool.balance(...a),
    allowance: (...a: any[]) => mockPool.allowance(...a),
    prepareApprove: (...a: any[]) => mockPool.prepareApprove(...a),
    prepareDeposit: (...a: any[]) => mockPool.prepareDeposit(...a),
    confirmDeposit: (...a: any[]) => mockPool.confirmDeposit(...a),
  },
}));
jest.mock('../wallet', () => ({
  ensureGas: jest.fn().mockResolvedValue(true),
  signTransaction: jest.fn().mockResolvedValue('0xsigned'),
  broadcastTransaction: jest.fn().mockResolvedValue({ hash: '0xdep' }),
  getProvider: () => ({ waitForTransaction: jest.fn().mockResolvedValue({ status: 1 }) }),
}));
jest.mock('../walletApi', () => ({ getAuthHeaders: jest.fn().mockResolvedValue({}) }));
jest.mock('@react-native-async-storage/async-storage', () => ({ getItem: jest.fn(), setItem: jest.fn() }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { ensureEscrowFunding } from '../escrowFunding';
// eslint-disable-next-line import/first
import { publishFailureMessage } from '../createAdFunding';

const word = (v: bigint) => v.toString(16).padStart(64, '0');
const E18 = 10n ** 18n;

async function failedPublish(prepareDepositRefusal: object): Promise<string> {
  mockPool.balance.mockResolvedValue({ success: true, data: { available: '0', locked: '0' } });
  mockPool.allowance.mockResolvedValue({ success: true, data: { allowance: (100n * E18).toString() } });
  mockPool.prepareDeposit.mockResolvedValue(prepareDepositRefusal);
  const err = await ensureEscrowFunding({
    token: 'USDT', chainId: 56, amountBaseUnits: (10n * E18).toString(), walletAddress: '0xSELLER', gasAction: 'deposit',
  }).then(() => null, (e: unknown) => e);
  expect(err).not.toBeNull();
  return publishFailureMessage(err, { symbol: 'USDT', decimals: 18, depositedThisAttempt: false });
}

it('an insufficient-balance revert on the deposit prepare still reads as "not enough USDT"', async () => {
  const raw = 'estimate gas (would revert): execution reverted: 0xe450d38c' + word(1n) + word(5n * E18) + word(10n * E18);
  const body = await failedPublish({ success: false, status: 400, message: raw });
  expect(body).toBe(
    "Your wallet doesn't have enough USDT to back this ad. It needs 10 USDT, but your wallet holds 5 USDT " +
      'on this network. Top up your USDT, then publish.',
  );
});

it('any other refusal is mapped copy, never the raw server body', async () => {
  const body = await failedPublish({ success: false, status: 502, message: '<html>502 Bad Gateway</html>' });
  expect(body).toBe('Prepare deposit failed');
});
