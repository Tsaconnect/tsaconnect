import client from './client';
import type { ApiResponse, User } from '@/types';

export async function login(email: string, password: string) {
  const { data } = await client.post<ApiResponse<{ token: string; userId: string; role: string }>>('/auth/login', { email, password });
  return data;
}

export async function getProfile() {
  const { data } = await client.get<ApiResponse<User>>('/users/profile');
  return data;
}
