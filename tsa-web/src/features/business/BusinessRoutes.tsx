import { Navigate, Outlet, Route, Routes, useLocation, useParams } from 'react-router-dom';
import { withMode } from '@/components/shell/mode';
import { Developers } from './Developers';
import { Liquidity } from './Liquidity';
import { Overview } from './Overview';
import { PaymentLinks } from './PaymentLinks';
import { PaymentDrawer } from './PaymentDrawer';
import { Payments } from './Payments';
import { Payouts } from './Payouts';
import { Reconciliation } from './Reconciliation';
import { OpenLinkDrawer } from './OpenLinkDrawer';
import { OpenLinkPoster } from './OpenLinkPoster';

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
        <Route path="links" element={<PaymentLinks />}>
          <Route path="open/:id" element={<OpenLinkDrawer />} />
          <Route path=":id" element={<PaymentDrawer list="links" />} />
        </Route>
        {/* The poster is a page of its own (no app frame), laid out to print. */}
        <Route path="links/open/:id/poster" element={<OpenLinkPoster />} />
        <Route path="payouts" element={<Payouts />} />
        <Route path="liquidity" element={<Liquidity />} />
        <Route path="reconciliation" element={<Reconciliation />} />
        <Route path="developers" element={<Developers />} />
        <Route path="*" element={<Navigate to="overview" replace />} />
      </Route>
    </Routes>
  );
}
