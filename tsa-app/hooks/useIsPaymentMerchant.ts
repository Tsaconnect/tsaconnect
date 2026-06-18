// hooks/useIsPaymentMerchant.ts
//
// Thin hook that reads the current user's Payment Merchant status on mount.
// Wraps getMyPmStatus() (services/p2p.ts) which calls GET /p2p/pm/me.
//
// Contract:
//   - While loading: { isPm: false, loading: true }
//   - On success:    { isPm: <from server>, loading: false }
//   - On any error:  { isPm: false, loading: false } (fail-open for UX safety)
//
// The backend is dormant-safe: if PM/Nomba is unconfigured it returns
// { isPm: false } 200 — this hook always resolves cleanly.

import { useEffect, useState } from 'react';
import { getMyPmStatus } from '../services/p2p';

export interface IsPaymentMerchantState {
  isPm: boolean;
  loading: boolean;
}

export function useIsPaymentMerchant(): IsPaymentMerchantState {
  const [isPm, setIsPm] = useState(false);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    getMyPmStatus()
      .then((status) => {
        if (!cancelled) {
          setIsPm(status.isPm);
        }
      })
      .catch(() => {
        // getMyPmStatus already swallows errors and returns {isPm:false};
        // this catch is an extra safety net.
        if (!cancelled) {
          setIsPm(false);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, []);

  return { isPm, loading };
}
