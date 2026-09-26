import { QRCodeSVG } from 'qrcode.react';
import { cn } from '@/lib/cn';

/** A QR code on white whatever the theme (scanners want dark on light), named for screen readers. */
export function QrCode({ value, size, label, className }: { value: string; size: number; label: string; className?: string }) {
  return (
    <div className={cn('rounded-md border border-hairline bg-white p-2', className)}>
      <QRCodeSVG value={value} size={size} marginSize={1} role="img" aria-label={label} className="block h-auto max-w-full" />
    </div>
  );
}
