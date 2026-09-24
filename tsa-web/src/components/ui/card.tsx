import type { HTMLAttributes, ReactNode } from 'react';
import { cn } from '@/lib/cn';

export const Card = ({ className, ...p }: HTMLAttributes<HTMLDivElement>) => (
  <div className={cn('rounded-lg border border-hairline bg-surface shadow-[var(--shadow-sm)]', className)} {...p} />
);

/** The signature start-edge rail (the app's AccentCard). */
export const AccentCard = ({ className, children, ...p }: HTMLAttributes<HTMLDivElement>) => (
  <Card className={cn('relative overflow-hidden pl-1', className)} {...p}>
    <span aria-hidden className="absolute inset-y-0 left-0 w-1 [background:var(--rail)]" />
    {children}
  </Card>
);

export const CardHeader = ({ title, description, actions }: { title: ReactNode; description?: ReactNode; actions?: ReactNode }) => (
  <div className="flex items-start gap-3 border-b border-hairline px-5 py-4">
    <div className="min-w-0 flex-1">
      <h3 className="text-[15px] leading-[22px] font-semibold">{title}</h3>
      {description && <p className="mt-0.5 text-[13px] leading-[18px] text-text-secondary">{description}</p>}
    </div>
    {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
  </div>
);

export const CardBody = ({ className, ...p }: HTMLAttributes<HTMLDivElement>) => <div className={cn('px-5 py-4', className)} {...p} />;
