import { Navigate, Outlet, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { withMode } from '@/components/shell/mode';
import { Developers } from './Developers';
import { LinksComingSoon } from './LinksComingSoon';
import { Overview } from './Overview';
import { PaymentDrawer } from './PaymentDrawer';
import { Payments } from './Payments';
import { Payouts } from './Payouts';

/** Only live and test exist: any other mode in the URL is the same page in live. */
function ModeGuard() {
  const { mode } = useParams();
  const { pathname, search } = useLocation();
  if (mode !== 'live' && mode !== 'test') return <Navigate to={withMode(pathname, search, 'live')} replace />;
  return <Outlet />;
}

export function BusinessRoutes() {
  return (
    <Routes>
      <Route index element={<Navigate to="live/overview" replace />} />
      <Route path=":mode" element={<ModeGuard />}>
        <Route index element={<Navigate to="overview" replace />} />
        <Route path="overview" element={<Overview />} />
        <Route path="payments" element={<Payments />}>
          <Route path=":id" element={<PaymentDrawer />} />
        </Route>
        <Route path="links" element={<LinksComingSoon />} />
        <Route path="payouts" element={<Payouts />} />
        <Route path="developers" element={<Developers />} />
        <Route path="*" element={<Navigate to="overview" replace />} />
      </Route>
    </Routes>
  );
}
