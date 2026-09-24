import { lazy, Suspense } from 'react';
import { Navigate, Route, Routes } from 'react-router-dom';
import { RequireSession } from '@/session/session';
import { DEFAULT_HOME } from '@/session/context';
import { Skeleton } from '@/components/ui/skeleton';
import { SignIn } from '@/features/auth/SignIn';
import { SignInCode } from '@/features/auth/SignInCode';
import { Account } from '@/features/account/Account';
import { BusinessRoutes } from '@/features/business/BusinessRoutes';

// The public docs are long and mostly visited on their own: loaded on demand.
const Docs = lazy(() => import('@/features/docs/Docs').then((m) => ({ default: m.Docs })));

function DocsLoading() {
  return (
    <div className="min-h-full bg-background" role="status" aria-label="Loading">
      <div className="h-14 bg-slate-900" />
      <div className="mx-auto grid max-w-[1200px] gap-4 px-4 pt-12 sm:px-7">
        <Skeleton className="h-3 w-24" />
        <Skeleton className="h-8 w-80 max-w-full" />
        <Skeleton className="h-4 w-full max-w-[72ch]" />
        <Skeleton className="h-4 w-2/3 max-w-[72ch]" />
      </div>
    </div>
  );
}

export function AppRoutes() {
  return (
    <Routes>
      <Route path="/signin" element={<SignIn />} />
      <Route path="/signin/code" element={<SignInCode />} />
      <Route path="/docs" element={<Suspense fallback={<DocsLoading />}><Docs /></Suspense>} />
      <Route path="/account" element={<RequireSession><Account /></RequireSession>} />
      <Route path="/business/*" element={<RequireSession><BusinessRoutes /></RequireSession>} />
      <Route path="*" element={<Navigate to={DEFAULT_HOME} replace />} />
    </Routes>
  );
}
