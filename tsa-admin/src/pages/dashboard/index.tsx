import { useQuery } from '@tanstack/react-query';
import { getDashboardStats } from '@/api/stats';
import { StatCard } from '@/components/shared/stat-card';
import { PageHeader } from '@/components/shared/page-header';
import { Users, Package, ShieldCheck, ShoppingCart } from 'lucide-react';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { useNavigate } from 'react-router-dom';

export default function DashboardPage() {
  const navigate = useNavigate();
  const { data, isLoading } = useQuery({
    queryKey: ['admin-stats'],
    queryFn: getDashboardStats,
    refetchInterval: 60_000,
  });

  const stats = data?.data;

  const pendingActions = [
    { label: 'Advert Requests', count: stats?.pendingAdverts ?? 0, path: '/advert-requests', color: 'bg-red-50 text-red-600' },
    { label: 'Deposit Requests', count: stats?.pendingDeposits ?? 0, path: '/deposits', color: 'bg-amber-50 text-amber-600' },
    { label: 'Verifications', count: stats?.pendingVerifications ?? 0, path: '/verifications', color: 'bg-blue-50 text-blue-600' },
  ];

  return (
    <div>
      <PageHeader title="Dashboard" description="Overview of your marketplace" />

      {isLoading ? (
        <div className="grid grid-cols-4 gap-4 mb-6">
          {Array.from({ length: 4 }).map((_, i) => <Skeleton key={i} className="h-28" />)}
        </div>
      ) : (
        <div className="grid grid-cols-4 gap-4 mb-6">
          <StatCard title="Total Users" value={stats?.totalUsers ?? 0} icon={Users} />
          <StatCard title="Total Products" value={stats?.totalProducts ?? 0} icon={Package} />
          <StatCard title="Total Orders" value={stats?.totalOrders ?? 0} icon={ShoppingCart} />
          <StatCard title="Pending Approvals" value={(stats?.pendingVerifications ?? 0) + (stats?.pendingDeposits ?? 0) + (stats?.pendingAdverts ?? 0)} icon={ShieldCheck} description="Needs attention" />
        </div>
      )}

      <div className="grid grid-cols-3 gap-4">
        {pendingActions.map((action) => (
          <Card
            key={action.label}
            className="cursor-pointer hover:shadow-md transition-shadow"
            onClick={() => navigate(action.path)}
          >
            <CardContent className="flex items-center justify-between p-6">
              <span className="text-sm font-medium text-slate-700">{action.label}</span>
              <span className={`inline-flex h-8 w-8 items-center justify-center rounded-full text-sm font-bold ${action.color}`}>
                {action.count}
              </span>
            </CardContent>
          </Card>
        ))}
      </div>
    </div>
  );
}
