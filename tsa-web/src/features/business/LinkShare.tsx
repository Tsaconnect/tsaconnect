import { useCallback, useEffect, useRef, useState } from 'react';
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

async function drawCard(qrBox: HTMLElement | null, card: ShareCard): Promise<File> {
  const svg = qrBox?.querySelector<SVGSVGElement>('svg[role="img"]');
  if (!svg) throw new Error('QR code not on screen');
  const blob = await shareCardBlob(svg, card);
  return new File([blob], shareCardFileName(card.businessName, card.test), { type: 'image/png' });
}

type CardImage = { key: string; file: Promise<File>; ready?: File };
type Action = 'share' | 'download';

const isClosedSheet = (err: unknown) => err instanceof DOMException && err.name === 'AbortError';

/**
 * A link four ways: copy it, share the QR card with the message through the
 * device's share sheet (into WhatsApp, say — its wa.me link can't carry an
 * image), send the message alone on WhatsApp, or save the QR card. For
 * one-time and reusable payment links alike.
 */
export function LinkShare({ url, message, card, qrSize = 132 }: { url: string; message: string; card: Omit<ShareCard, 'url'>; qrSize?: number }) {
  const qrRef = useRef<HTMLDivElement>(null);
  const [imagesShareable] = useState(canShareImages);
  const [running, setRunning] = useState<Action | null>(null);
  const image = useRef<CardImage | null>(null);
  const savedUrls = useRef(new Map<string, ReturnType<typeof setTimeout>>());
  const { businessName, headline, detail, test } = card;

  // The card is drawn before the tap and kept: Safari opens the share sheet
  // only when navigator.share is called straight from the tap, not after the
  // wait for a drawing.
  const cardImage = useCallback((): CardImage => {
    const key = JSON.stringify([url, businessName, headline, detail, test]);
    if (image.current?.key === key) return image.current;
    const made: CardImage = { key, file: drawCard(qrRef.current, { url, businessName, headline, detail, test }) };
    made.file.then(
      (file) => { made.ready = file; },
      () => { if (image.current === made) image.current = null; }, // the next tap tries again
    );
    image.current = made;
    return made;
  }, [url, businessName, headline, detail, test]);

  useEffect(() => {
    if (imagesShareable) cardImage();
  }, [imagesShareable, cardImage]);

  // A saved file's address is let go a minute on, or when the link closes:
  // straight away, some browsers haven't started the download yet.
  useEffect(() => {
    const urls = savedUrls.current;
    return () => {
      for (const [href, timer] of urls) {
        clearTimeout(timer);
        URL.revokeObjectURL(href);
      }
      urls.clear();
    };
  }, []);

  async function run(action: Action, work: Promise<unknown>, failure: string) {
    setRunning(action);
    try {
      await work;
    } catch (err) {
      if (!isClosedSheet(err)) toast.error(failure); // closing the share sheet is a choice
    } finally {
      setRunning(null);
    }
  }

  function share() {
    if (running) return;
    const { ready, file } = cardImage();
    const send = (image: File) => navigator.share({ files: [image], text: message });
    // Not drawn yet (a tap straight after opening): Safari may refuse the share after the wait.
    void run('share', ready ? send(ready) : file.then(send), "We couldn't share the QR code. Copy the link instead.");
  }

  function save(image: File) {
    const href = URL.createObjectURL(image);
    const a = document.createElement('a');
    a.href = href;
    a.download = image.name;
    a.click();
    const urls = savedUrls.current;
    urls.set(href, setTimeout(() => {
      URL.revokeObjectURL(href);
      urls.delete(href);
    }, 60_000));
  }

  function download() {
    if (running) return;
    void run('download', cardImage().file.then(save), "We couldn't save the QR code. Try again.");
  }

  const warmUp = () => { cardImage(); };

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
              <Button variant="secondary" size="sm" onClick={share} onPointerEnter={warmUp} onFocus={warmUp} loading={running === 'share'}>
                {running !== 'share' && <Share2 className="size-4" aria-hidden />}Share with QR code
              </Button>
            )}
            <Button asChild variant="secondary" size="sm">
              <a href={whatsAppUrl(message)} target="_blank" rel="noopener noreferrer">
                <MessageCircle className="size-4" aria-hidden />Share on WhatsApp
              </a>
            </Button>
            <Button variant="ghost" size="sm" onClick={download} onPointerEnter={warmUp} onFocus={warmUp} loading={running === 'download'}>
              {running !== 'download' && <Download className="size-4" aria-hidden />}Download QR
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
