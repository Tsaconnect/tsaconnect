import client from './client';
import type { ApiResponse, User, PaginationMeta } from '@/types';

export async function getPendingVerifications(params: { page?: number; limit?: number }) {
  const { data } = await client.get<ApiResponse<{ users: User[]; pagination: PaginationMeta }>>('/users', {
    params: { ...params, verificationStatus: 'pending' },
  });
  return data;
}

export async function approveVerification(userId: string) {
  const { data } = await client.post<ApiResponse>(`/verification/approve/${userId}`);
  return data;
}

export async function rejectVerification(userId: string, reason?: string) {
  const { data } = await client.post<ApiResponse>(`/verification/reject/${userId}`, { reason });
  return data;
}
