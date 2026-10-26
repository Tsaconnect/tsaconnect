// One merchant gate for every screen that shows merchant-only UI. The users.role
// column holds a single value, so an admin or super admin approved as a merchant
// keeps their staff role: the server's merchantApproved flag (an approved
// merchant application) is what makes them one.
export function isMerchantAccount(
  role: string | null | undefined,
  merchantApproved: boolean,
): boolean {
  const r = (role || '').toLowerCase();
  if (r === 'merchant') return true;
  const isStaff = r === 'admin' || r === 'super_admin';
  return isStaff && merchantApproved;
}
