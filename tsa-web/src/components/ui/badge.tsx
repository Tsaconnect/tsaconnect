import type { ReactNode } from 'react';
import { cn } from '@/lib/cn';

const tones = {
  live: 'bg-success-bg text-success-strong',
  test: 'bg-accent-muted text-accent-strong',
  neutral: 'bg-surface-alt text-text-secondary',
};

export const Badge = ({ tone, children, className }: { tone: keyof typeof tones; children: ReactNode; className?: string }) => (
  <span className={cn('inline-flex items-center rounded-[5px] px-1.5 py-0.5 text-overline font-bold uppercase', tones[tone], className)}>
    {children}
  </span>
);
