// hooks/useUserRole.ts
//
// Which roles count as a merchant for in-screen gating (e.g. the Trade & Earn
// CTA). Roles are normalized to lowercase. Backend ships them as 'merchant',
// 'admin', or 'super_admin'; the marketplace flow historically stored
// 'superadmin' (no underscore), so callers compare against the set below to
// stay tolerant of both shapes.

/**
 * Roles that are allowed to publish P2P sell ads on the backend
 * (`p2p_handler.go:CreateAd`). Centralised so gating UI agrees with the
 * server's 403 response.
 */
export const MERCHANT_ROLES = new Set(['merchant', 'admin', 'super_admin', 'superadmin']);

export function isMerchantRole(role: string | null | undefined): boolean {
  return !!role && MERCHANT_ROLES.has(role.toLowerCase());
}
