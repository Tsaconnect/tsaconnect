import client from './client';
import type { ApiResponse, AdminStats } from '@/types';

export async function getDashboardStats() {
  const { data } = await client.get<ApiResponse<AdminStats>>('/admin/stats');
  return data;
}
