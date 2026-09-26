import { cn } from '@/lib/cn';

export const Skeleton = ({ className }: { className?: string }) => (
  <div aria-hidden className={cn('animate-pulse rounded-sm bg-surface-alt', className)} />
);
