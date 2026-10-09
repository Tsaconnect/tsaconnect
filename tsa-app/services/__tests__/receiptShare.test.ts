/* eslint-disable import/first -- jest hoists the mocks above the imports they replace. */

/**
 * Share receipt sends the receipt itself: its image, downloaded and handed to
 * the share sheet — and, where the phone can't share a file or the image can't
 * be had, the words and the link as before.
 */
const mockRequireOptional = jest.fn();
jest.mock('expo', () => ({ __esModule: true, requireOptionalNativeModule: (name: string) => mockRequireOptional(name) }));

const mockDownload = jest.fn();
jest.mock('expo-file-system', () => ({
  __esModule: true,
  Paths: { cache: 'cache-dir' },
  File: Object.assign(
    jest.fn().mockImplementation((dir: string, name: string) => ({ uri: `file:///${dir}/${name}` })),
    { downloadFileAsync: (...args: unknown[]) => mockDownload(...args) },
  ),
}));

const mockShareAsync = jest.fn(async () => {});
jest.mock('expo-sharing', () => ({ __esModule: true, shareAsync: (...args: unknown[]) => (mockShareAsync as any)(...args) }));

import { Platform, Share } from 'react-native';
import { receiptImageUrl, shareReceipt } from '../receiptShare';

const RECEIPT = 'https://tsaconnectworld.com/r/TSA-8D4DQ-Y4DMN';
const MESSAGE = `Receipt: ₦1,010.11 paid to Sky Air · TSA-8D4DQ-Y4DMN. Verify it: ${RECEIPT}`;
const setOS = (os: 'ios' | 'android') => Object.defineProperty(Platform, 'OS', { get: () => os, configurable: true });

let share: jest.SpyInstance;
beforeEach(() => {
  jest.clearAllMocks();
  jest.useRealTimers();
  mockDownload.mockImplementation(async (_url: string, file: { uri: string }) => file);
  share = jest.spyOn(Share, 'share').mockResolvedValue({ action: 'sharedAction' } as any);
  jest.spyOn(console, 'warn').mockImplementation(() => {});
});

it('names the receipt image on the same site as the receipt page', () => {
  expect(receiptImageUrl(RECEIPT)).toBe('https://tsaconnectworld.com/og/r/TSA-8D4DQ-Y4DMN.png');
});

it('with the share module, downloads the image and shares the PNG file', async () => {
  setOS('android');
  mockRequireOptional.mockReturnValue({});
  await shareReceipt(RECEIPT, MESSAGE);
  expect(mockRequireOptional).toHaveBeenCalledWith('ExpoSharing');
  expect(mockDownload).toHaveBeenCalledWith('https://tsaconnectworld.com/og/r/TSA-8D4DQ-Y4DMN.png', expect.objectContaining({ uri: 'file:///cache-dir/TSA-8D4DQ-Y4DMN.png' }), { idempotent: true });
  expect(mockShareAsync).toHaveBeenCalledWith('file:///cache-dir/TSA-8D4DQ-Y4DMN.png', expect.objectContaining({ mimeType: 'image/png' }));
  expect(share).not.toHaveBeenCalled();
});

it('a copy from an earlier share is overwritten, never re-sent stale', async () => {
  setOS('android');
  mockRequireOptional.mockReturnValue({});
  await shareReceipt(RECEIPT, MESSAGE);
  expect(mockDownload).toHaveBeenCalledWith(expect.any(String), expect.anything(), { idempotent: true });
});

it('a download that hangs gives way to the link after 8 s', async () => {
  jest.useFakeTimers();
  setOS('android');
  mockRequireOptional.mockReturnValue({});
  mockDownload.mockReturnValue(new Promise(() => {}));
  const done = shareReceipt(RECEIPT, MESSAGE);
  await jest.advanceTimersByTimeAsync(8_000);
  await done;
  expect(mockShareAsync).not.toHaveBeenCalled();
  expect(share).toHaveBeenCalledWith({ message: MESSAGE });
});

it('on iOS without the module, shares the downloaded file through the system sheet', async () => {
  setOS('ios');
  mockRequireOptional.mockReturnValue(null);
  await shareReceipt(RECEIPT, MESSAGE);
  expect(share).toHaveBeenCalledWith({ url: 'file:///cache-dir/TSA-8D4DQ-Y4DMN.png', message: MESSAGE });
  expect(mockShareAsync).not.toHaveBeenCalled();
});

it('on an older Android build without the module, sends the words and the link as before', async () => {
  setOS('android');
  mockRequireOptional.mockReturnValue(null);
  await shareReceipt(RECEIPT, MESSAGE);
  expect(mockDownload).not.toHaveBeenCalled();
  expect(share).toHaveBeenCalledWith({ message: MESSAGE });
});

it('when the image cannot be had, still shares the link', async () => {
  setOS('android');
  mockRequireOptional.mockReturnValue({});
  mockDownload.mockRejectedValue(new Error('offline'));
  await shareReceipt(RECEIPT, MESSAGE);
  expect(mockShareAsync).not.toHaveBeenCalled();
  expect(share).toHaveBeenCalledWith({ message: MESSAGE });
});
