import * as D from '@radix-ui/react-dialog';
import { X } from 'lucide-react';
import { useRef, type ReactNode } from 'react';

const keepOpen = (e: Event) => e.preventDefault();
const FIELD = 'input:not([type=radio]):not([type=checkbox]):not([type=hidden]), textarea, select';

/**
 * A modal dialog. `dismissible={false}` leaves no way out but the footer's own
 * buttons: no close button, and neither Escape nor a click outside closes it.
 * `focusFirstField` puts the cursor in the first text field when it opens — only
 * then: fields that change while it is open (a choice swapping them) never pull
 * focus away from what the person is using.
 */
export function Dialog({ open, onOpenChange, title, description, children, footer, dismissible = true, focusFirstField }: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  description?: string;
  children: ReactNode;
  footer?: ReactNode;
  dismissible?: boolean;
  focusFirstField?: boolean;
}) {
  const content = useRef<HTMLDivElement>(null);
  const onOpenAutoFocus = (e: Event) => {
    const field = content.current?.querySelector<HTMLElement>(FIELD);
    if (!field) return;
    e.preventDefault();
    field.focus();
  };
  return (
    <D.Root open={open} onOpenChange={onOpenChange}>
      <D.Portal>
        <D.Overlay className="fixed inset-0 z-40 bg-[var(--overlay)]" />
        <D.Content
          ref={content}
          onOpenAutoFocus={focusFirstField ? onOpenAutoFocus : undefined}
          className="fixed left-1/2 top-[12vh] z-50 flex max-h-[76vh] w-[min(520px,calc(100vw-32px))] -translate-x-1/2 flex-col rounded-lg bg-surface shadow-[var(--shadow-lg)] focus:outline-none"
          {...(description ? {} : { 'aria-describedby': undefined })}
          {...(dismissible ? {} : { onEscapeKeyDown: keepOpen, onInteractOutside: keepOpen })}
        >
          <div className="flex items-center gap-3 border-b border-hairline px-6 py-4">
            <D.Title className="flex-1 text-[16px] leading-[22px] font-semibold">{title}</D.Title>
            {dismissible && (
              <D.Close aria-label="Close" className="rounded-sm p-1 text-text-secondary hover:bg-surface-alt hover:text-text-primary">
                <X className="size-4" aria-hidden />
              </D.Close>
            )}
          </div>
          <div className="overflow-auto px-6 py-5">
            {description && <D.Description className="mb-4 text-body-sm text-text-secondary">{description}</D.Description>}
            {children}
          </div>
          {footer && <div className="flex justify-end gap-2 rounded-b-lg border-t border-hairline bg-background px-6 py-3">{footer}</div>}
        </D.Content>
      </D.Portal>
    </D.Root>
  );
}
