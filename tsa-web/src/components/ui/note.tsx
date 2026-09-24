import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

// Text on each tint uses the web-only *-strong shade (WCAG AA).
const tones = {
  warning: 'bg-warning-bg text-warning-strong',
  gold: 'bg-accent-muted text-accent-strong',
  info: 'bg-info-bg text-info-strong',
  success: 'bg-success-bg text-success-strong',
};

/** A tinted aside: a warning before a risky step, a confirmation, a hint. */
export function Note({ tone, icon, children, className, id }: { tone: keyof typeof tones; icon?: ReactNode; children: ReactNode; className?: string; id?: string }) {
  return (
    <div id={id} className={cn('flex items-start gap-2.5 rounded-[10px] px-3.5 py-3 text-[13px] leading-[19px]', tones[tone], className)}>
      {icon && <span className="mt-px shrink-0 [&>svg]:size-4" aria-hidden>{icon}</span>}
      <div className="min-w-0 flex-1">{children}</div>
    </div>
  );
}
