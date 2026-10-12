const mockWait = jest.fn();
jest.mock('../wallet', () => ({ getProvider: () => ({ waitForTransaction: (...a: any[]) => mockWait(...a) }) }));
jest.mock('../p2p', () => ({ p2pChainKey: (id: number) => `chain-${id}` }));

// eslint-disable-next-line import/first -- jest.mock factories hoist above this import.
import { TxRevertedError, waitMined } from '../txWait';

beforeEach(() => mockWait.mockReset());

it('resolves when the receipt succeeded', async () => {
  mockWait.mockResolvedValue({ status: 1 });
  await expect(waitMined(146, '0xa')).resolves.toBeUndefined();
  expect(mockWait).toHaveBeenCalledWith('0xa');
});

it('rejects with TxRevertedError when the tx mined but reverted (status 0)', async () => {
  mockWait.mockResolvedValue({ status: 0 });
  await expect(waitMined(146, '0xa')).rejects.toBeInstanceOf(TxRevertedError);
});

it('rejects when no receipt comes back', async () => {
  mockWait.mockResolvedValue(null);
  await expect(waitMined(146, '0xa')).rejects.toBeInstanceOf(TxRevertedError);
});

it('times out with the caller’s label and clears its timer', async () => {
  jest.useFakeTimers();
  mockWait.mockReturnValue(new Promise(() => {}));
  const p = waitMined(146, '0xa', { timeoutMs: 1000, label: 'Approve' });
  const assertion = expect(p).rejects.toThrow('Approve confirmation timed out');
  jest.advanceTimersByTime(1000);
  await assertion;
  expect(jest.getTimerCount()).toBe(0);
  jest.useRealTimers();
});

it('polls the provider that accepted the broadcast when given one', async () => {
  const own = jest.fn().mockResolvedValue({ status: 1 });
  await waitMined(146, '0xb', { provider: { waitForTransaction: own } });
  expect(own).toHaveBeenCalledWith('0xb');
  expect(mockWait).not.toHaveBeenCalled();
});
