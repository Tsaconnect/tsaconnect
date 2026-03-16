import { useLocation } from 'react-router-dom';
import { Input } from '@/components/ui/input';
import { Bell, Search } from 'lucide-react';
import { Button } from '@/components/ui/button';

const PAGE_TITLES: Record<string, { title: string; breadcrumb: string }> = {
  '/dashboard': { title: 'Dashboard', breadcrumb: 'Overview' },
  '/users': { title: 'Users', breadcrumb: 'Management' },
  '/products': { title: 'Products', breadcrumb: 'Management' },
  '/categories': { title: 'Categories', breadcrumb: 'Management' },
  '/orders': { title: 'Orders', breadcrumb: 'Management' },
  '/advert-requests': { title: 'Advert Requests', breadcrumb: 'Approvals' },
  '/deposits': { title: 'Deposits', breadcrumb: 'Approvals' },
  '/verifications': { title: 'Verifications', breadcrumb: 'Approvals' },
  '/settings': { title: 'Settings', breadcrumb: 'System' },
};

export function Header() {
  const location = useLocation();
  const pageInfo = PAGE_TITLES[location.pathname] || { title: 'Page', breadcrumb: '' };

  return (
    <header className="flex h-14 items-center justify-between border-b border-slate-200 bg-white px-6">
      <div>
        <p className="text-[11px] text-slate-400">{pageInfo.breadcrumb}</p>
        <h1 className="text-base font-semibold text-slate-900">{pageInfo.title}</h1>
      </div>
      <div className="flex items-center gap-3">
        <div className="relative">
          <Search className="absolute left-2.5 top-2.5 h-4 w-4 text-slate-400" />
          <Input placeholder="Search..." className="w-52 pl-9 text-sm" disabled />
        </div>
        <Button variant="ghost" size="icon" className="relative" disabled>
          <Bell className="h-4 w-4 text-slate-500" />
        </Button>
      </div>
    </header>
  );
}
