import { Copy } from 'lucide-react';
import { copyText } from '@/lib/clipboard';
import { Button } from './button';

/** A value to hand on (a key, a secret, a payment link) in a box that selects whole on click, with Copy. */
export function CopyBox({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center gap-2">
      <code className="min-w-0 flex-1 rounded-sm border border-hairline bg-background px-3 py-2.5 font-mono text-[12.5px] break-all select-all">{value}</code>
      <Button size="sm" onClick={() => copyText(value)} aria-label={`Copy ${label}`}>
        <Copy className="size-3.5" aria-hidden />Copy
      </Button>
    </div>
  );
}
