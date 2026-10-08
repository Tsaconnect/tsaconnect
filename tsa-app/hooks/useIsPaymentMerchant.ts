// hooks/useIsPaymentMerchant.ts
//
// Thin hook that reads the current user's Payment Merchant status on mount.
// Wraps getMyPmStatus() (services/p2p.ts) which calls GET /p2p/pm/me.
//
// Contract:
//   - While loading: { isPm: false, status: '', activationNeedsId: true, liquidityAvailable: false, loading: true }
//   - On success:    { isPm, status, activationNeedsId, kind, poolChainId, liquidityAvailable } from the server, loading: false
//   - On any error:  the loading defaults with loading: false (fail-open)
//
// `kind` is the caller's marketplace standing ('lp' | 'merchant' | ...); `liquidityAvailable`
// is false until the pool chain is configured (the LP CTA stays hidden).
//
// `status` is the raw PM status ('' | 'pending' | 'approved' | 'active' | ...).
// `isPm` is true only for a live (active) PM; callers use status === 'approved'
// to surface the KYC-activation CTA, and `activationNeedsId` to decide whether
// that activation asks for a BVN/NIN. A `reload` is returned so a screen can
// refresh after activation without a remount.
//
// The backend is dormant-safe: if the PM stack is unconfigured it returns
// { isPm: false } 200 — this hook always resolves cleanly.

import { useCallback, useEffect, useState } from 'react';
import { getMyPmStatus } from '../services/p2p';

export interface IsPaymentMerchantState {
  isPm: boolean;
  status: string;
  activationNeedsId: boolean;
  kind?: string;
  poolChainId?: number;
  liquidityAvailable: boolean;
  loading: boolean;
  reload: () => void;
}

export function useIsPaymentMerchant(): IsPaymentMerchantState {
  const [isPm, setIsPm] = useState(false);
  const [status, setStatus] = useState('');
  const [activationNeedsId, setActivationNeedsId] = useState(true);
  const [kind, setKind] = useState<string | undefined>(undefined);
  const [poolChainId, setPoolChainId] = useState<number | undefined>(undefined);
  const [liquidityAvailable, setLiquidityAvailable] = useState(false);
  const [loading, setLoading] = useState(true);
  const [nonce, setNonce] = useState(0);

  const reload = useCallback(() => setNonce((n) => n + 1), []);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    getMyPmStatus()
      .then((s) => {
        if (!cancelled) {
          setIsPm(s.isPm);
          setStatus(s.status);
          setActivationNeedsId(s.activationNeedsId);
          setKind(s.kind);
          setPoolChainId(s.poolChainId);
          setLiquidityAvailable(!!s.liquidityAvailable);
        }
      })
      .catch(() => {
        // getMyPmStatus already swallows errors and returns {isPm:false};
        // this catch is an extra safety net.
        if (!cancelled) {
          setIsPm(false);
          setStatus('');
          setActivationNeedsId(true);
          setKind(undefined);
          setPoolChainId(undefined);
          setLiquidityAvailable(false);
        }
      })
      .finally(() => {
        if (!cancelled) setLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [nonce]);

  return { isPm, status, activationNeedsId, kind, poolChainId, liquidityAvailable, loading, reload };
}
