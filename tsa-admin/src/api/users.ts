import client from './client';
import type { ApiResponse, User, PaginationMeta } from '@/types';

export async function getUsers(params: { page?: number; limit?: number; search?: string; role?: string }) {
  const { data } = await client.get<ApiResponse<{ users: User[]; pagination: PaginationMeta }>>('/users', { params });
  return data;
}

export async function updateUserRole(userId: string, role: string) {
  const { data } = await client.patch<ApiResponse>(`/users/${userId}/role`, { role });
  return data;
}
