import type { ReactNode } from 'react';
import { AppShell } from '@/components/shell/AppShell';
import { Card } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { ErrorState } from '@/components/ui/states';
import type { OwnMerchant } from './api';
import { NoMerchant } from './NoMerchant';
import { isNoMerchant, useMerchant } from './useMerchant';

function PageSkeleton() {
  return (
    <div className="grid gap-4" aria-hidden>
      <Card className="grid gap-3 p-5">
        <Skeleton className="h-4 w-40" />
        <Skeleton className="h-3 w-72" />
      </Card>
      <Card className="grid gap-4 p-5">
        {Array.from({ length: 4 }, (_, i) => <Skeleton key={i} className="h-5 w-full" />)}
      </Card>
    </div>
  );
}

/**
 * A business page in the signed-in shell, rendered only once the owner's merchant
 * is known: skeletons while it loads, the apply page when there is none, a retry
 * when it could not be read.
 */
export function BusinessPage({ title, actions, children }: {
  title: string;
  actions?: ReactNode;
  children: (merchant: OwnMerchant) => ReactNode;
}) {
  const q = useMerchant();
  let body: ReactNode;
  if (q.data) body = children(q.data);
  else if (q.isPending) body = <PageSkeleton />;
  else if (isNoMerchant(q.error)) body = <NoMerchant />;
  else {
    body = (
      <Card>
        <ErrorState
          title="We couldn't load your business"
          body={q.error?.message ?? 'Please try again.'}
          onRetry={() => void q.refetch()}
        />
      </Card>
    );
  }
  return (
    <AppShell section="business" title={title} actions={q.data ? actions : undefined} merchant={q.data ?? (q.isError ? null : undefined)}>
      {body}
    </AppShell>
  );
}
