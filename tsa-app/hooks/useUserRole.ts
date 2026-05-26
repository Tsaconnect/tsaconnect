// hooks/useUserRole.ts
//
// Resolve the current user's role for in-screen gating (e.g. show "Become a
// merchant" instead of "+ Sell" on the P2P pages for non-merchants). Fetches
// the freshest value from /profile, persists it under AsyncStorage `role`,
// and falls back to the cached value if the network read fails — matches the
// pattern used in screens/marketplace.tsx so the role label stays consistent
// across the app.
//
// Roles are normalized to lowercase. Backend ships them as 'merchant',
// 'admin', or 'super_admin'; the marketplace flow historically stored
// 'superadmin' (no underscore), so callers should compare against the set
// below to stay tolerant of both shapes.

import { useCallback, useEffect, useState } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import api from '../components/services/api';

/**
 * Roles that are allowed to publish P2P sell ads on the backend
 * (`p2p_handler.go:CreateAd`). Centralised so gating UI agrees with the
 * server's 403 response.
 */
export const MERCHANT_ROLES = new Set(['merchant', 'admin', 'super_admin', 'superadmin']);

export function isMerchantRole(role: string | null | undefined): boolean {
  return !!role && MERCHANT_ROLES.has(role.toLowerCase());
}

export interface UserRoleState {
  role: string | null;
  isMerchant: boolean;
  loading: boolean;
  /** Re-fetch from /profile; useful after onboarding flows. */
  refresh: () => Promise<void>;
}

export function useUserRole(): UserRoleState {
  const [role, setRole] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  const refresh = useCallback(async () => {
    try {
      const token = await api.getStoredToken();
      if (token) {
        api.setToken(token);
        const profileRes = await api.getProfile();
        if (profileRes.success && profileRes.data?.role) {
          const r = String(profileRes.data.role).toLowerCase();
          setRole(r);
          await AsyncStorage.setItem('role', r);
          return;
        }
      }
      const stored = await AsyncStorage.getItem('role');
      setRole(stored?.toLowerCase() || null);
    } catch {
      const stored = await AsyncStorage.getItem('role');
      setRole(stored?.toLowerCase() || null);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => {
    refresh();
  }, [refresh]);

  return { role, isMerchant: isMerchantRole(role), loading, refresh };
}
