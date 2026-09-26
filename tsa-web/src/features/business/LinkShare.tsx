import { QRCodeSVG } from 'qrcode.react';
import { MessageCircle } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { CopyBox } from '@/components/ui/copy-box';
import type { Payment } from './api';
import { whatsAppUrl } from './display';

/** A payment link three ways: copy it, send it on WhatsApp, or let the customer scan it. */
export function LinkShare({ businessName, payment: p }: { businessName: string; payment: Payment }) {
  return (
    <div className="grid gap-3">
      <CopyBox label="payment link" value={p.checkoutUrl} />
      <div className="flex flex-wrap items-center gap-4">
        {/* White behind the code whatever the theme: scanners want dark on light. */}
        <div className="rounded-md border border-hairline bg-white p-2">
          <QRCodeSVG value={p.checkoutUrl} size={132} marginSize={1} role="img" aria-label="QR code for the payment link" />
        </div>
        <div className="grid min-w-0 flex-1 gap-2">
          <Button asChild variant="secondary" size="sm" className="justify-self-start">
            <a href={whatsAppUrl(businessName, p)} target="_blank" rel="noopener noreferrer">
              <MessageCircle className="size-4" aria-hidden />Share on WhatsApp
            </a>
          </Button>
          <p className="text-caption text-text-secondary">Or let your customer scan the code with their phone camera.</p>
        </div>
      </div>
    </div>
  );
}
