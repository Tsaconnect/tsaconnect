import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getUsers, updateUserRole } from '@/api/users';
import { PageHeader } from '@/components/shared/page-header';
import { DataTable } from '@/components/shared/data-table';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { ROLE_LABELS } from '@/lib/constants';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { User } from '@/types';

export default function SettingsPage() {
  const [page, setPage] = useState(1);
  const queryClient = useQueryClient();
  const canManageRoles = usePermission('users.manage_roles');

  const { data, isLoading } = useQuery({
    queryKey: ['settings-users', page],
    queryFn: () => getUsers({ page, limit: 20 }),
  });

  const mutation = useMutation({
    mutationFn: ({ userId, role }: { userId: string; role: string }) => updateUserRole(userId, role),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['settings-users'] });
      toast.success('User role updated');
    },
    onError: () => toast.error('Failed to update role'),
  });

  const users = data?.data?.users ?? [];
  const pagination = data?.data?.pagination;

  const columns: ColumnDef<User>[] = [
    { accessorKey: 'name', header: 'Name' },
    { accessorKey: 'email', header: 'Email' },
    {
      accessorKey: 'role',
      header: 'Role',
      cell: ({ row }) => {
        if (!canManageRoles) return ROLE_LABELS[row.original.role] || row.original.role;
        return (
          <Select
            value={row.original.role}
            onValueChange={(v) => mutation.mutate({ userId: row.original.id, role: v })}
          >
            <SelectTrigger className="w-36">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="user">User</SelectItem>
              <SelectItem value="merchant">Merchant</SelectItem>
              <SelectItem value="support">Support</SelectItem>
              <SelectItem value="admin">Admin</SelectItem>
              <SelectItem value="super_admin">Super Admin</SelectItem>
            </SelectContent>
          </Select>
        );
      },
    },
    { accessorKey: 'createdAt', header: 'Joined', cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString() },
  ];

  return (
    <div>
      <PageHeader title="Settings" description="Manage user roles and system settings" />
      <DataTable columns={columns} data={users} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} />
    </div>
  );
}
