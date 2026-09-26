import type { ReactNode } from 'react';

/** A form's refusal, announced to screen readers. */
export function FormError({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-md bg-danger-bg px-3.5 py-2.5 text-[13px] leading-[18px] text-danger-strong">
      {children}
    </div>
  );
}
