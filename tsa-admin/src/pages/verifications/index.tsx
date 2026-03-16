import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getPendingVerifications, approveVerification, rejectVerification } from '@/api/verification';
import { PageHeader } from '@/components/shared/page-header';
import { Button } from '@/components/ui/button';
import { Card, CardContent } from '@/components/ui/card';
import { Skeleton } from '@/components/ui/skeleton';
import { CheckCircle2, XCircle, User as UserIcon } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { User } from '@/types';

export default function VerificationsPage() {
  const [page, setPage] = useState(1);
  const queryClient = useQueryClient();
  const canApprove = usePermission('verifications.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['verifications', page],
    queryFn: () => getPendingVerifications({ page, limit: 20 }),
  });

  const approveMutation = useMutation({
    mutationFn: (userId: string) => approveVerification(userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['verifications'] });
      queryClient.invalidateQueries({ queryKey: ['stats'] });
      toast.success('Verification approved');
    },
    onError: () => toast.error('Failed to approve verification'),
  });

  const rejectMutation = useMutation({
    mutationFn: (userId: string) => rejectVerification(userId),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['verifications'] });
      queryClient.invalidateQueries({ queryKey: ['stats'] });
      toast.success('Verification rejected');
    },
    onError: () => toast.error('Failed to reject verification'),
  });

  const users: User[] = data?.data?.users ?? [];
  const pagination = data?.data?.pagination;
  const isPending = approveMutation.isPending || rejectMutation.isPending;

  if (isLoading) return <Skeleton className="h-64 w-full" />;

  return (
    <div>
      <PageHeader title="Verifications" description="Review pending user verification requests" />

      {users.length === 0 ? (
        <p className="text-sm text-slate-500 mt-4">No pending verifications.</p>
      ) : (
        <div className="space-y-3">
          {users.map((user) => (
            <Card key={user.id}>
              <CardContent className="p-4 flex items-center justify-between">
                <div className="flex items-center gap-3">
                  {user.profilePhoto?.url ? (
                    <img src={user.profilePhoto.url} alt={user.name} className="h-10 w-10 rounded-full object-cover" />
                  ) : (
                    <div className="h-10 w-10 rounded-full bg-slate-100 flex items-center justify-center">
                      <UserIcon className="h-5 w-5 text-slate-400" />
                    </div>
                  )}
                  <div>
                    <p className="font-medium text-slate-900">{user.name}</p>
                    <p className="text-xs text-slate-500">{user.email} · {user.phoneNumber}</p>
                    <p className="text-xs text-slate-400">Joined {new Date(user.createdAt).toLocaleDateString()}</p>
                  </div>
                </div>
                {canApprove && (
                  <div className="flex gap-2">
                    <Button
                      size="sm"
                      onClick={() => approveMutation.mutate(user.id)}
                      disabled={isPending}
                    >
                      <CheckCircle2 className="mr-1 h-4 w-4" /> Approve
                    </Button>
                    <Button
                      size="sm"
                      variant="destructive"
                      onClick={() => rejectMutation.mutate(user.id)}
                      disabled={isPending}
                    >
                      <XCircle className="mr-1 h-4 w-4" /> Reject
                    </Button>
                  </div>
                )}
              </CardContent>
            </Card>
          ))}

          {pagination && pagination.totalPages > 1 && (
            <div className="flex justify-center gap-2 mt-4">
              <Button variant="outline" size="sm" disabled={page <= 1} onClick={() => setPage(page - 1)}>Previous</Button>
              <span className="text-sm text-slate-500 self-center">Page {page} of {pagination.totalPages}</span>
              <Button variant="outline" size="sm" disabled={page >= pagination.totalPages} onClick={() => setPage(page + 1)}>Next</Button>
            </div>
          )}
        </div>
      )}
    </div>
  );
}
