export type Role = 'super_admin' | 'admin' | 'support' | 'user' | 'merchant';

export interface User {
  id: string;
  name: string;
  username: string;
  email: string;
  role: Role;
  phoneNumber: string;
  country: string;
  state?: string;
  city?: string;
  address: string;
  profilePhoto?: { url: string; publicId: string };
  referralCode?: string;
  verificationStatus: string;
  accountStatus: string;
  lastLogin?: string;
  walletAddress?: string;
  createdAt: string;
  updatedAt: string;
}

export interface Product {
  id: string;
  userId: string;
  name: string;
  description: string;
  price: number;
  stock: number;
  category?: string;
  categoryName?: string;
  images?: { id: string; url: string; publicId?: string; order: number }[];
  status: 'active' | 'inactive' | 'sold_out' | 'pending_review';
  type: 'Product' | 'Service';
  isFeatured: boolean;
  views: number;
  sales: number;
  createdAt: string;
  updatedAt: string;
}

export interface Category {
  id: string;
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string;
  icon?: string;
  image?: string;
  color: string;
  isActive: boolean;
  order: number;
  productCount: number;
  children?: Category[];
  createdAt?: string;
  updatedAt?: string;
}

export interface OrderItem {
  productId: string;
  name: string;
  quantity: number;
  unitPrice: number;
  total: number;
}

export interface Order {
  id: string;
  buyerId: string;
  sellerId: string;
  items: OrderItem[];
  total: number;
  currency: string;
  status: 'pending' | 'processing' | 'shipped' | 'completed' | 'cancelled';
  shippingAddress?: string;
  notes?: string;
  createdAt: string;
  updatedAt: string;
}

export interface Deposit {
  id: string;
  userId: string;
  amount: number;
  currency: string;
  status: 'pending' | 'approved' | 'rejected';
  proofUrl?: string;
  adminNote?: string;
  reviewedBy?: string;
  reviewedAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface AdminStats {
  totalUsers: number;
  totalProducts: number;
  pendingVerifications: number;
  pendingDeposits: number;
  pendingAdverts: number;
  totalOrders: number;
  revenue: number;
}

export interface PaginationMeta {
  page: number;
  limit: number;
  total: number;
  totalPages: number;
}

export interface ApiResponse<T = unknown> {
  success: boolean;
  message?: string;
  data?: T;
  errors?: { field: string; message: string }[];
}
