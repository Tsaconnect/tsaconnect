import type { ReactNode } from 'react';
import { Button } from './button';

export function EmptyState({ title, body, action, icon }: { title: string; body?: ReactNode; action?: ReactNode; icon?: ReactNode }) {
  return (
    <div className="grid place-items-center gap-2 px-6 py-14 text-center">
      {icon && <div className="mb-1 grid size-11 place-items-center rounded-full bg-accent-muted text-accent-strong">{icon}</div>}
      <p className="text-title3 font-semibold">{title}</p>
      {body && <p className="max-w-md text-body-sm text-text-secondary">{body}</p>}
      {action && <div className="mt-2">{action}</div>}
    </div>
  );
}

export function ErrorState({ title, body, onRetry }: { title: string; body?: ReactNode; onRetry?: () => void }) {
  return (
    <div role="alert" className="grid place-items-center gap-2 px-6 py-14 text-center">
      <p className="text-title3 font-semibold">{title}</p>
      {body && <p className="max-w-md text-body-sm text-text-secondary">{body}</p>}
      {onRetry && (
        <Button variant="secondary" size="sm" className="mt-2" onClick={onRetry}>
          Try again
        </Button>
      )}
    </div>
  );
}
