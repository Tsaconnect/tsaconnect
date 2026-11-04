const mockStore = new Map<string, string>();
jest.mock('expo-secure-store', () => ({
  __esModule: true,
  WHEN_UNLOCKED_THIS_DEVICE_ONLY: 'w',
  getItemAsync: jest.fn(async (k: string) => mockStore.get(k) ?? null),
  setItemAsync: jest.fn(async (k: string, v: string) => void mockStore.set(k, v)),
  deleteItemAsync: jest.fn(async (k: string) => void mockStore.delete(k)),
}));

/* eslint-disable import/first */
import * as SecureStore from 'expo-secure-store';
import {
  birthDateForInput,
  forgetBuyerId,
  formatBirthDateInput,
  parseBuyerId,
  readBuyerId,
  saveBuyerId,
} from '../buyerId';

const NOW = new Date(Date.UTC(2026, 10, 4));

beforeEach(() => mockStore.clear());

describe('parseBuyerId', () => {
  it('accepts an 11-digit BVN and turns DD/MM/YYYY into YYYY-MM-DD', () => {
    expect(parseBuyerId({ type: 'bvn', number: '222 1234 5678', birthDate: '15/04/1990' }, NOW)).toEqual({
      id: { type: 'bvn', number: '22212345678', birthDate: '1990-04-15' },
    });
  });

  it('accepts someone who turns 18 today', () => {
    expect('id' in parseBuyerId({ type: 'nin', number: '12345678901', birthDate: '04/11/2008' }, NOW)).toBe(true);
  });

  it.each([
    [{ type: 'bvn', number: '1234567890', birthDate: '15/04/1990' }, 'number', 'Your BVN is 11 digits.'],
    [{ type: 'nin', number: '12345', birthDate: '15/04/1990' }, 'number', 'Your NIN is 11 digits.'],
    [{ type: 'bvn', number: '12345678901', birthDate: '1990-04-15' }, 'birthDate', 'Enter your date of birth as DD/MM/YYYY.'],
    [{ type: 'bvn', number: '12345678901', birthDate: '31/02/1990' }, 'birthDate', 'That date does not exist.'],
    [{ type: 'bvn', number: '12345678901', birthDate: '05/11/2008' }, 'birthDate', 'You must be 18 or over to buy.'],
  ] as const)('refuses %o on %s', (input, field, message) => {
    expect(parseBuyerId(input, NOW)).toEqual({ field, message });
  });
});

describe('birth date input', () => {
  it('adds the slashes as she types', () => {
    expect(formatBirthDateInput('1')).toBe('1');
    expect(formatBirthDateInput('150')).toBe('15/0');
    expect(formatBirthDateInput('15041990')).toBe('15/04/1990');
    expect(formatBirthDateInput('15/04/19901')).toBe('15/04/1990');
  });

  it('shows a saved date back in the form', () => {
    expect(birthDateForInput('1990-04-15')).toBe('15/04/1990');
  });
});

describe('device store', () => {
  const id = { type: 'bvn' as const, number: '22212345678', birthDate: '1990-04-15' };

  it('keeps the ID per wallet and account, in the secure store, on this device only', async () => {
    await saveBuyerId('0xABC', 'user-1', id);
    await expect(readBuyerId('0xabc', 'user-1')).resolves.toEqual(id);
    await expect(readBuyerId('0xdef', 'user-1')).resolves.toBeNull();
    expect(SecureStore.setItemAsync).toHaveBeenCalledWith(
      'buyerId.v1.0xabc',
      JSON.stringify({ ...id, account: 'user-1' }),
      { keychainAccessible: 'w' },
    );
  });

  it("never hands one account another account's ID on a shared phone and wallet", async () => {
    await saveBuyerId('0xabc', 'user-1', id);
    await expect(readBuyerId('0xabc', 'user-2')).resolves.toBeNull();
    await expect(readBuyerId('0xabc', '')).resolves.toBeNull();
  });

  it('refuses to save with no account to bind it to', async () => {
    await expect(saveBuyerId('0xabc', '', id)).rejects.toThrow();
  });

  it('reads a damaged entry as none, so she is asked again', async () => {
    mockStore.set('buyerId.v1.0xabc', '{"type":"passport","account":"user-1"}');
    await expect(readBuyerId('0xabc', 'user-1')).resolves.toBeNull();
    mockStore.set('buyerId.v1.0xabc', 'not json');
    await expect(readBuyerId('0xabc', 'user-1')).resolves.toBeNull();
  });

  it('forgets it', async () => {
    await saveBuyerId('0xabc', 'user-1', id);
    await forgetBuyerId('0xabc');
    await expect(readBuyerId('0xabc', 'user-1')).resolves.toBeNull();
  });
});
