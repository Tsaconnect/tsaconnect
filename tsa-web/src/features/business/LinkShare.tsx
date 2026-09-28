import { useRef, useState } from 'react';
import { Download, MessageCircle, Share2 } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { CopyBox } from '@/components/ui/copy-box';
import { QrCode } from '@/components/ui/qr-code';
import { toast } from '@/components/ui/toaster';
import { whatsAppUrl } from './display';
import { shareCardBlob, shareCardFileName, type ShareCard } from './shareCard';

/** Whether this browser can hand an image to the share sheet (phones, most desktops). */
function canShareImages(): boolean {
  try {
    const probe = new File([new Uint8Array(1)], 'probe.png', { type: 'image/png' });
    return typeof navigator.share === 'function' && navigator.canShare?.({ files: [probe] }) === true;
  } catch {
    return false;
  }
}

/**
 * A link four ways: copy it, share the QR card with the message through the
 * device's share sheet (into WhatsApp, say — its wa.me link can't carry an
 * image), send the message alone on WhatsApp, or save the QR card. For
 * one-time and reusable payment links alike.
 */
export function LinkShare({ url, message, card, qrSize = 132 }: { url: string; message: string; card: Omit<ShareCard, 'url'>; qrSize?: number }) {
  const qrRef = useRef<HTMLDivElement>(null);
  const [imagesShareable] = useState(canShareImages);
  const [busy, setBusy] = useState(false);

  async function makeImage(): Promise<File> {
    const svg = qrRef.current?.querySelector('svg');
    if (!svg) throw new Error('QR code not on screen');
    const blob = await shareCardBlob(svg, { ...card, url });
    return new File([blob], shareCardFileName(card.businessName), { type: 'image/png' });
  }

  async function run(action: (image: File) => Promise<void>, failure: string) {
    setBusy(true);
    try {
      await action(await makeImage());
    } catch (err) {
      // Closing the share sheet is a choice, not a failure.
      if (!(err instanceof DOMException && err.name === 'AbortError')) toast.error(failure);
    } finally {
      setBusy(false);
    }
  }

  const share = () => run((image) => navigator.share({ files: [image], text: message }), "We couldn't share the QR code. Copy the link instead.");

  const download = () =>
    run(async (image) => {
      const href = URL.createObjectURL(image);
      const a = document.createElement('a');
      a.href = href;
      a.download = image.name;
      a.click();
      setTimeout(() => URL.revokeObjectURL(href), 0);
    }, "We couldn't save the QR code. Try again.");

  return (
    <div className="grid gap-3">
      <CopyBox label="payment link" value={url} />
      <div className="flex flex-wrap items-center gap-4">
        <div ref={qrRef}>
          <QrCode value={url} size={qrSize} label="QR code for the payment link" />
        </div>
        <div className="grid min-w-0 flex-1 gap-2">
          <div className="flex flex-wrap gap-2">
            {imagesShareable && (
              <Button variant="secondary" size="sm" onClick={share} disabled={busy}>
                <Share2 className="size-4" aria-hidden />Share with QR code
              </Button>
            )}
            <Button asChild variant="secondary" size="sm">
              <a href={whatsAppUrl(message)} target="_blank" rel="noopener noreferrer">
                <MessageCircle className="size-4" aria-hidden />Share on WhatsApp
              </a>
            </Button>
            <Button variant="ghost" size="sm" onClick={download} disabled={busy}>
              <Download className="size-4" aria-hidden />Download QR
            </Button>
          </div>
          <p className="text-caption text-text-secondary">
            {imagesShareable
              ? 'Share with QR code sends the message and a QR image together. Or let your customer scan the code with their phone camera.'
              : 'Download QR to attach it to a chat or print it. Or let your customer scan the code with their phone camera.'}
          </p>
        </div>
      </div>
    </div>
  );
}
