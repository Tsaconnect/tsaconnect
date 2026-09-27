import { MessageCircle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { CopyBox } from '@/components/ui/copy-box';
import { QrCode } from '@/components/ui/qr-code';
import { whatsAppUrl } from './display';

/**
 * A link three ways: copy it, send it on WhatsApp with `message`, or let the
 * customer scan it. For one-time and reusable payment links alike.
 */
export function LinkShare({ url, message, qrSize = 132 }: { url: string; message: string; qrSize?: number }) {
  return (
    <div className="grid gap-3">
      <CopyBox label="payment link" value={url} />
      <div className="flex flex-wrap items-center gap-4">
        <QrCode value={url} size={qrSize} label="QR code for the payment link" />
        <div className="grid min-w-0 flex-1 gap-2">
          <Button asChild variant="secondary" size="sm" className="justify-self-start">
            <a href={whatsAppUrl(message)} target="_blank" rel="noopener noreferrer">
              <MessageCircle className="size-4" aria-hidden />Share on WhatsApp
            </a>
          </Button>
          <p className="text-caption text-text-secondary">Or let your customer scan the code with their phone camera.</p>
        </div>
      </div>
    </div>
  );
}
