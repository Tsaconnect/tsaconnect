import * as D from '@radix-ui/react-dialog';
import { X } from 'lucide-react';
import type { ReactNode } from 'react';

/** A right-hand panel over the page (payment detail). */
export function Drawer({ open, onOpenChange, title, children }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  children: ReactNode;
}) {
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-[var(--overlay)]" />
        <D.Content
          className="fixed inset-y-0 right-0 z-50 flex w-[min(470px,100vw)] flex-col bg-surface shadow-[var(--shadow-lg)] focus:outline-none"
          aria-describedby={undefined}
        >
          <div className="flex items-center gap-3 border-b border-hairline px-6 py-4">
            <D.Title className="min-w-0 flex-1 truncate text-title3 font-semibold">{title}</D.Title>
            <D.Close aria-label="Close" className="rounded-sm p-1 text-text-secondary hover:bg-surface-alt hover:text-text-primary">
              <X className="size-4" aria-hidden />
            </D.Close>
          </div>
          <div className="flex-1 overflow-auto px-6 py-5">{children}</div>
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
