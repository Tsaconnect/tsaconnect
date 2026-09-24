import { cloneElement, useId, type ReactElement } from 'react';
import * as Label from '@radix-ui/react-label';

type Control = ReactElement<{ id?: string; 'aria-describedby'?: string; 'aria-invalid'?: boolean }>;

/** A labelled control with its hint or error wired up for screen readers. */
export function Field({ label, hint, error, children }: { label: string; hint?: string; error?: string; children: Control }) {
  const id = useId();
  const describe = error ? `${id}-error` : hint ? `${id}-hint` : undefined;
  return (
    <div className="grid gap-1.5">
      <Label.Root htmlFor={id} className="text-[13px] leading-[18px] font-semibold text-text-secondary">{label}</Label.Root>
      {cloneElement(children, { id, 'aria-describedby': describe, 'aria-invalid': error ? true : undefined })}
      {error ? (
        <p id={describe} className="text-caption text-danger">{error}</p>
      ) : hint ? (
        <p id={describe} className="text-caption text-text-secondary">{hint}</p>
      ) : null}
    </div>
  );
}
