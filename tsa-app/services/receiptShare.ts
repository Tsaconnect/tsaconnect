// services/receiptShare.ts
//
// Share receipt, as the receipt itself: the branded 1080×1350 image the
// landing site draws for a live receipt (tsa-landing netlify/edge-functions/
// receipt-og.ts, /og/r/TSA-XXXXX-XXXXX.png) is downloaded to the phone's cache
// and handed to the system share sheet, so WhatsApp, Status or Gallery get a
// picture, not a line of text.
//
// The native share module (expo-sharing) is only in builds made after it was
// added: an older binary running this through an OTA has none. So it is looked
// up, never imported up front (importing it there would throw), and without it
// iOS still shares the file through React Native's own sheet while Android —
// whose built-in sheet takes text only — sends the words and the link, as
// before. Any failure (offline, image not there yet) also falls back to text.
import { Platform, Share } from 'react-native';
import { requireOptionalNativeModule } from 'expo';
import { File, Paths } from 'expo-file-system';

/**
 * The receipt image's URL for a receipt page URL already checked by
 * safeReceiptUrl (https, our host, /r/…). A dev build may point it at a local
 * copy of the landing site (EXPO_PUBLIC_RECEIPT_IMAGE_ORIGIN) to try an image
 * change before it is deployed; a release build never reads that.
 */
export function receiptImageUrl(receiptUrl: string): string {
  const { origin, pathname } = new URL(receiptUrl);
  const devOrigin = __DEV__ ? process.env.EXPO_PUBLIC_RECEIPT_IMAGE_ORIGIN : undefined;
  return `${devOrigin || origin}/og${pathname}.png`;
}

/** The code a receipt URL names, for the file's name: TSA-XXXXX-XXXXX, or "receipt". */
function fileNameFor(receiptUrl: string): string {
  const code = /\/r\/(TSA-[0-9A-Z]{5}-[0-9A-Z]{5})$/.exec(new URL(receiptUrl).pathname)?.[1];
  return `${code ?? 'receipt'}.png`;
}

/**
 * Past this, the link is shared instead. A receipt's first render takes the
 * landing site ~10 s (a cold function); after that the CDN serves it in ~1–3 s.
 */
const DOWNLOAD_TIMEOUT_MS = 20_000;

async function downloadReceiptImage(receiptUrl: string): Promise<string> {
  // idempotent overwrites the copy from an earlier share: a refund changes the image.
  const download = File.downloadFileAsync(receiptImageUrl(receiptUrl), new File(Paths.cache, fileNameFor(receiptUrl)), { idempotent: true });
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error('receipt image download timed out')), DOWNLOAD_TIMEOUT_MS);
  });
  try {
    return (await Promise.race([download, timeout])).uri;
  } finally {
    clearTimeout(timer);
  }
}

/**
 * Shares a live receipt: as its image wherever the phone can, else as `message`
 * (the words and the link). Resolves once the share sheet has closed.
 */
export async function shareReceipt(receiptUrl: string, message: string): Promise<void> {
  const nativeSharing = requireOptionalNativeModule('ExpoSharing') !== null;
  if (nativeSharing || Platform.OS === 'ios') {
    try {
      const uri = await downloadReceiptImage(receiptUrl);
      if (nativeSharing) {
        // Required only here, once the module is known to exist: loading it on a
        // binary without the native side throws.
        // eslint-disable-next-line @typescript-eslint/no-require-imports
        const Sharing = require('expo-sharing') as typeof import('expo-sharing');
        await Sharing.shareAsync(uri, { mimeType: 'image/png', UTI: 'public.png', dialogTitle: 'Share receipt' });
      } else {
        await Share.share({ url: uri, message });
      }
      return;
    } catch (err) {
      console.warn('Receipt image share failed, sharing the link instead:', err);
    }
  }
  await Share.share({ message });
}
