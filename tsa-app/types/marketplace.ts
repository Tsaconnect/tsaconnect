export interface Seller {
  sellerId: string;
  name: string;
  email: string;
  profileImage: string;
  rating?: number;
  location?: string;
  productCount?: number;
  description?: string;
  joinedDate?: string;
  totalSales?: number;
}

export interface Product {
  productId: string;
  name: string;
  price: number;
  image: string;
  subcategory: string;
  sellerId: string;
  description?: string;
  category?: string;
  stock?: number;
  isFeatured?: boolean;
  tags?: string[];
  createdAt?: string;
  // Promotion (price stays the original; see Product in components/services/api.ts)
  promoEnabled?: boolean;
  promoPrice?: number;
  promoDiscountPercent?: number;
  promoRequiredReferrals?: number;
  promoCountdownSeconds?: number;
}

export interface Subcategory {
  id: string;
  name: string;
  icon?: string;
  description?: string;
}

export interface SellerWithProducts extends Seller {
  products: Product[];
}

export interface SearchResult {
  sellers: SellerWithProducts[];
  products: Product[];
}