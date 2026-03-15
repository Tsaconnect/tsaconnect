import { useAuth } from './use-auth';
import { hasPermission, type Permission } from '@/lib/permissions';

export function usePermission(permission: Permission): boolean {
  const { user } = useAuth();
  if (!user) return false;
  return hasPermission(user.role, permission);
}
