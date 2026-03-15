import type { Role } from '@/types';

export type Permission =
  | 'dashboard.view'
  | 'users.view'
  | 'users.edit'
  | 'users.delete'
  | 'users.manage_roles'
  | 'products.view'
  | 'products.approve'
  | 'products.edit'
  | 'products.delete'
  | 'categories.view'
  | 'categories.create'
  | 'categories.edit'
  | 'categories.delete'
  | 'orders.view'
  | 'orders.update_status'
  | 'deposits.view'
  | 'deposits.approve'
  | 'verifications.view'
  | 'verifications.approve'
  | 'settings.view';

const ROLE_PERMISSIONS: Record<string, Permission[]> = {
  super_admin: [
    'dashboard.view', 'users.view', 'users.edit', 'users.delete', 'users.manage_roles',
    'products.view', 'products.approve', 'products.edit', 'products.delete',
    'categories.view', 'categories.create', 'categories.edit', 'categories.delete',
    'orders.view', 'orders.update_status',
    'deposits.view', 'deposits.approve',
    'verifications.view', 'verifications.approve',
    'settings.view',
  ],
  admin: [
    'dashboard.view', 'users.view', 'users.edit',
    'products.view', 'products.approve', 'products.edit', 'products.delete',
    'categories.view', 'categories.create', 'categories.edit',
    'orders.view', 'orders.update_status',
    'deposits.view', 'deposits.approve',
    'verifications.view', 'verifications.approve',
  ],
  support: [
    'dashboard.view', 'users.view',
    'products.view',
    'categories.view',
    'orders.view', 'orders.update_status',
    'deposits.view',
    'verifications.view',
  ],
};

export function hasPermission(role: Role, permission: Permission): boolean {
  const permissions = ROLE_PERMISSIONS[role];
  if (!permissions) return false;
  return permissions.includes(permission);
}

export function canAccessRoute(role: Role, path: string): boolean {
  const routePermissions: Record<string, Permission> = {
    '/dashboard': 'dashboard.view',
    '/users': 'users.view',
    '/products': 'products.view',
    '/categories': 'categories.view',
    '/orders': 'orders.view',
    '/advert-requests': 'products.view',
    '/deposits': 'deposits.view',
    '/verifications': 'verifications.view',
    '/settings': 'settings.view',
  };

  const permission = routePermissions[path];
  if (!permission) return true;
  return hasPermission(role, permission);
}
