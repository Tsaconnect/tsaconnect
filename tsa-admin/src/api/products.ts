import client from './client';
import type { ApiResponse, Product, PaginationMeta } from '@/types';

export async function getProducts(params: { page?: number; limit?: number; search?: string; status?: string; type?: string }) {
  const { data } = await client.get<ApiResponse<{ products: Product[]; pagination: PaginationMeta }>>('/products', { params });
  return data;
}

export async function getNonFeaturedProducts() {
  const { data } = await client.get<ApiResponse<Product[]>>('/products/non-featured');
  return data;
}

export async function toggleFeatured(productId: string) {
  const { data } = await client.patch<ApiResponse>(`/products/${productId}/featured`);
  return data;
}

export async function updateProduct(productId: string, updates: Partial<Product>) {
  const { data } = await client.put<ApiResponse>(`/products/${productId}`, updates);
  return data;
}

export async function deleteProduct(productId: string) {
  const { data } = await client.delete<ApiResponse>(`/products/${productId}`);
  return data;
}
