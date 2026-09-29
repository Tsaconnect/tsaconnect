import { LogOut } from 'lucide-react';
import { useSession } from '@/session/context';
import { AppShell } from '@/components/shell/AppShell';
import { Card, CardHeader } from '@/components/ui/card';
import { Button } from '@/components/ui/button';
import { Skeleton } from '@/components/ui/skeleton';
import { cn } from '@/lib/cn';

function Verification({ status }: { status: string }) {
  const verified = status === 'verified';
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1.5 rounded-full px-2.5 py-0.5 text-caption font-semibold',
        verified ? 'bg-success-bg text-success-strong' : 'bg-surface-alt text-text-secondary',
      )}
    >
      <span aria-hidden className="size-1.5 rounded-full bg-current" />
      {verified ? 'Verified' : 'Not verified'}
    </span>
  );
}

export function Account() {
  const { user, signOut } = useSession();
  const row = 'grid gap-0.5 py-3 sm:grid-cols-[160px_1fr] sm:gap-4';
  const verified = user?.verificationStatus === 'verified';
  return (
    <AppShell section="personal" title="Account">
      <section aria-label="Your account" className="max-w-2xl">
        <Card>
          <CardHeader title="Your account" description="The TSA Connect account you signed in with." />
          <dl className="divide-y divide-[var(--hairline)] px-5 text-body-sm">
            <div className={row}>
              <dt className="text-text-secondary">Name</dt>
              <dd>
                {user ? <p className="font-medium">{user.name}</p> : <Skeleton className="h-4 w-32" />}
                {/* A verified name is the one on the ID: changing it means verifying again, which only the app does for now. */}
                {verified && (
                  <p className="mt-0.5 text-caption text-text-secondary">To change your name, use the TSA Connect app — you'll verify your identity again.</p>
                )}
              </dd>
            </div>
            <div className={row}>
              <dt className="text-text-secondary">Email</dt>
              <dd className="font-medium break-all">{user ? user.email : <Skeleton className="h-4 w-48" />}</dd>
            </div>
            <div className={row}>
              <dt className="text-text-secondary">Verification</dt>
              <dd>{user ? <Verification status={user.verificationStatus} /> : <Skeleton className="h-5 w-20" />}</dd>
            </div>
          </dl>
          <div className="flex flex-wrap items-center justify-between gap-3 rounded-b-lg border-t border-hairline bg-background px-5 py-3.5">
            <p className="text-caption text-text-secondary">{verified ? 'Change your password' : 'Change your name or password'} in the TSA Connect app.</p>
            <Button variant="secondary" size="sm" onClick={() => void signOut()}>
              <LogOut className="size-4" aria-hidden />
              Sign out
            </Button>
          </div>
        </Card>
      </section>
    </AppShell>
  );
}
