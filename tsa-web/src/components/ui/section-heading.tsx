import type { ReactNode } from 'react';

/** A small caps heading between sections of a drawer. */
export const SectionHeading = ({ children }: { children: ReactNode }) => (
  <h3 className="mt-6 mb-2.5 text-overline font-semibold uppercase tracking-[.06em] text-text-secondary">{children}</h3>
);
