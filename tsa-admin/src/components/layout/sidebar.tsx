import { NavLink } from 'react-router-dom';
import { cn } from '@/lib/utils';
import { useAuth } from '@/hooks/use-auth';
import { canAccessRoute } from '@/lib/permissions';
import {
  LayoutDashboard, Users, Package, FolderTree, ShoppingCart,
  Megaphone, Wallet, ShieldCheck, Store, Settings, ChevronLeft, LogOut,
} from 'lucide-react';
import { Button } from '@/components/ui/button';
import { ROLE_LABELS } from '@/lib/constants';

interface SidebarProps {
  collapsed: boolean;
  onToggle: () => void;
}

const navGroups = [
  {
    label: 'Overview',
    items: [
      { path: '/dashboard', label: 'Dashboard', icon: LayoutDashboard },
    ],
  },
  {
    label: 'Management',
    items: [
      { path: '/users', label: 'Users', icon: Users },
      { path: '/products', label: 'Products', icon: Package },
      { path: '/categories', label: 'Categories', icon: FolderTree },
      { path: '/orders', label: 'Orders', icon: ShoppingCart },
    ],
  },
  {
    label: 'Approvals',
    items: [
      { path: '/advert-requests', label: 'Advert Requests', icon: Megaphone },
      { path: '/deposits', label: 'Deposits', icon: Wallet },
      { path: '/verifications', label: 'Verifications', icon: ShieldCheck },
      { path: '/merchant-requests', label: 'Merchant Requests', icon: Store },
    ],
  },
  {
    label: 'System',
    items: [
      { path: '/settings', label: 'Settings', icon: Settings },
    ],
  },
];

export function Sidebar({ collapsed, onToggle }: SidebarProps) {
  const { user, logout } = useAuth();

  return (
    <aside
      className={cn(
        'flex flex-col border-r border-slate-200 bg-white transition-all duration-200',
        collapsed ? 'w-16' : 'w-60'
      )}
    >
      {/* Logo */}
      <div className="flex h-14 items-center justify-between border-b border-slate-200 px-3">
        {!collapsed && (
          <div className="flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-blue-600 text-white font-bold text-sm">T</div>
            <span className="font-semibold text-slate-900">TSA Admin</span>
          </div>
        )}
        <Button variant="ghost" size="icon" onClick={onToggle} className="h-8 w-8">
          <ChevronLeft className={cn('h-4 w-4 transition-transform', collapsed && 'rotate-180')} />
        </Button>
      </div>

      {/* Nav */}
      <nav className="flex-1 overflow-y-auto p-2">
        {navGroups.map((group) => {
          const visibleItems = group.items.filter(
            (item) => user && canAccessRoute(user.role, item.path)
          );
          if (visibleItems.length === 0) return null;

          return (
            <div key={group.label} className="mb-4">
              {!collapsed && (
                <p className="mb-1 px-3 text-[10px] font-medium uppercase tracking-wider text-slate-400">
                  {group.label}
                </p>
              )}
              {visibleItems.map((item) => (
                <NavLink
                  key={item.path}
                  to={item.path}
                  className={({ isActive }) =>
                    cn(
                      'flex items-center gap-2 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                      isActive
                        ? 'bg-blue-50 text-blue-600'
                        : 'text-slate-600 hover:bg-slate-50 hover:text-slate-900',
                      collapsed && 'justify-center px-2'
                    )
                  }
                >
                  <item.icon className="h-4 w-4 shrink-0" />
                  {!collapsed && <span>{item.label}</span>}
                </NavLink>
              ))}
            </div>
          );
        })}
      </nav>

      {/* User section */}
      <div className="border-t border-slate-200 p-3">
        {!collapsed && user && (
          <div className="mb-2 flex items-center gap-2">
            <div className="flex h-8 w-8 items-center justify-center rounded-full bg-blue-600 text-xs font-semibold text-white">
              {user.name.charAt(0).toUpperCase()}
            </div>
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-slate-900">{user.name}</p>
              <p className="text-[10px] text-slate-400">{ROLE_LABELS[user.role] || user.role}</p>
            </div>
          </div>
        )}
        <Button variant="ghost" size={collapsed ? 'icon' : 'sm'} onClick={logout} className="w-full justify-start text-slate-600">
          <LogOut className="h-4 w-4" />
          {!collapsed && <span className="ml-2">Logout</span>}
        </Button>
      </div>
    </aside>
  );
}
