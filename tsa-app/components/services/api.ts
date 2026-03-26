// services/api.ts
import AsyncStorage from '@react-native-async-storage/async-storage';

import { API_BASE_URL } from '../../constants/api/config';

export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
  errors?: Array<{ field: string; message: string }>;
}

export interface SignupResponse {
  userId: string;
  token: string;
  role: string;
  emailVerified?: boolean;
  nextStep: 'email_verification' | 'identity_verification' | 'facial_verification' | 'complete';
}

export interface UploadResponse {
  url: string;
  publicId: string;
  format: string;
  size: number;
  width?: number;
  height?: number;
}

export interface MultipleUploadResponse {
  uploaded: Array<UploadResponse & { index: number }>;
  failed?: Array<{ index: number; error: string }>;
}
//Category
export interface Category {
  id: string;
  _id?: string;
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string | Category;
  icon?: string;
  color: string;
  isActive: boolean;
  order: number;
  productCount: number;
  children?: Category[];
  createdAt?: string;
  updatedAt?: string;
}

// Category Tree Node interface
export interface CategoryTreeNode extends Category {
  children: CategoryTreeNode[];
}

// Create Category Request interface
export interface CreateCategoryRequest {
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string;
  icon?: string;
  color?: string;
  order?: number;
  isActive?: boolean;
  image?: any; // For FormData upload
}

// Update Category Request interface
export interface UpdateCategoryRequest {
  title?: string;
  description?: string;
  type?: 'Product' | 'Service' | 'Both';
  parentCategory?: string | null;
  icon?: string;
  color?: string;
  order?: number;
  isActive?: boolean;
  image?: any; // For FormData upload
}

// Category Response interface
export interface CategoryResponse {
  success: boolean;
  message: string;
  data: Category | Category[];
  pagination?: {
    page: number;
    limit: number;
    total: number;
    pages: number;
  };
}

// Category Tree Response interface
export interface CategoryTreeResponse {
  success: boolean;
  data: CategoryTreeNode[];
}

// Category with Details interface
export interface CategoryWithDetails extends Category {
  parent?: Category;
  children: Category[];
  productCount: number;
}


// Dashboard-specific interfaces
export interface Asset {
  _id: string;
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  isSelected: boolean;
  isHidden: boolean;
  details?: {
    type: string;
    chain?: string;
    address?: string;
  };
  currentPrice?: number;
  priceChange24h?: number;
}

export interface PortfolioTotals {
  balance: number;
  usdValue: number;
  dailyChange: number;
}

export interface AssetsResponse {
  assets: Asset[];
  totals: PortfolioTotals;
  lastUpdated: string;
}

export interface SelectAssetRequest {
  assetId: string;
}

export interface RefreshResponse {
  success: boolean;
  message: string;
  data?: any;
}

export interface AssetVisibilityResponse {
  success: boolean;
  message: string;
  data?: any;
}

export interface Notification {
  id: string;
  userId: string;
  type: string;
  title: string;
  message: string;
  data: Record<string, any>;
  channel: string;
  isRead: boolean;
  readAt: string | null;
  createdAt: string;
}

export interface NotificationPreferences {
  muteNotifications: boolean;
  muteEmail: boolean;
}

export interface Product {
  id: string;
  _id?: string;
  userId?: string;
  name: string;
  description: string;
  price: number;
  stock: number;
  category?: string | {
    id: string;
    title: string;
    icon?: string;
    color?: string;
  };
  categoryName?: string;
  location: string;
  phoneNumber: string;
  email: string;
  companyName?: string;
  images: Array<{
    id?: string;
    url?: string;
    publicId?: string;
    order?: number;
  }>;
  attributes?: Array<{
    name: string;
    value: string;
  }>;
  status: 'active' | 'inactive' | 'sold_out' | 'pending_review';
  type: 'Product' | 'Service';
  isFeatured?: boolean;
  views: number;
  sales: number;
  rating: {
    average: number;
    count: number;
  } | null;
  shippingSameCity?: number;
  shippingSameState?: number;
  shippingSameCountry?: number;
  shippingInternational?: number;
  createdAt: string;
  updatedAt: string;
}


class APIService {
  private token: string | null = null;

  // Set token for authenticated requests
  setToken(token: string) {
    this.token = token;
  }

  // Clear token
  clearToken() {
    this.token = null;
  }

  // Get headers for requests
  private getHeaders(contentType: string = 'application/json'): HeadersInit {
    const headers: HeadersInit = {
      'Content-Type': contentType,
    };

    if (this.token) {
      headers['Authorization'] = `Bearer ${this.token}`;
    }

    return headers;
  }
  //Merchant Product methods
  async getMerchantProducts(): Promise<ApiResponse<{ products: Product[]; pagination: any }>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/user`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<{ products: Product[]; pagination: any }>(response);
    } catch (error: any) {
      console.error('Get merchant products error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch merchant products',
      };
    }
  }
  async getNonFeaturedProducts(): Promise<ApiResponse<Product[]>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/non-featured`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<Product[]>(response);
    } catch (error: any) {
      console.error('Get non-featured products error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch non-featured products',
      };
    }
  }

  async toggleFeatured(productId: string): Promise<ApiResponse<Product>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/${productId}/featured`, {
        method: 'PATCH',
        headers: this.getHeaders(),
      });

      return this.handleResponse<Product>(response);
    } catch (error: any) {
      console.error('Toggle featured error:', error);
      return {
        success: false,
        message: error.message || 'Failed to toggle featured status',
      };
    }
  }
  // Handle API response
  private async handleResponse<T>(response: Response): Promise<ApiResponse<T>> {
    let data;

    try {
      data = await response.json();
    } catch (error) {
      return {
        success: false,
        message: `HTTP ${response.status}: ${response.statusText}`,
      };
    }

    if (!response.ok) {
      return {
        success: false,
        message: data.message || `Request failed with status ${response.status}`,
        errors: data.errors,
      };
    }

    return {
      success: true,
      ...data,
    };
  }

  // Convert blob to base64
  private blobToBase64(blob: Blob): Promise<string> {
    return new Promise((resolve, reject) => {
      const reader = new FileReader();
      reader.onloadend = () => {
        const result = reader.result as string;
        resolve(result);
      };
      reader.onerror = reject;
      reader.readAsDataURL(blob);
    });
  }

  // ==================== DASHBOARD ENDPOINTS ====================

  // Get portfolio assets
  async getPortfolioAssets(): Promise<ApiResponse<AssetsResponse>> {
    this.getHeaders();
    console.log('Fetching portfolio assets with headers:', this.getHeaders());
    try {
      const response = await fetch(`${API_BASE_URL}/assets`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<AssetsResponse>(response);
    } catch (error: any) {
      console.error('Get portfolio assets error:', error);
      return {
        success: false,
        message: error.message || 'Failed to load portfolio data.',
      };
    }
  }

  // Refresh portfolio assets
  async refreshPortfolioAssets(): Promise<ApiResponse<RefreshResponse>> {
    try {
      const response = await fetch(`${API_BASE_URL}/assets/refresh`, {
        method: 'POST',
        headers: this.getHeaders(),
      });

      return this.handleResponse<RefreshResponse>(response);
    } catch (error: any) {
      console.error('Refresh portfolio assets error:', error);
      return {
        success: false,
        message: error.message || 'Failed to refresh assets.',
      };
    }
  }

  // Select an asset
  async selectAsset(assetId: string): Promise<ApiResponse<SelectAssetRequest>> {
    try {
      const response = await fetch(`${API_BASE_URL}/assets/select`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({ assetId }),
      });

      return this.handleResponse<SelectAssetRequest>(response);
    } catch (error: any) {
      console.error('Select asset error:', error);
      return {
        success: false,
        message: error.message || 'Failed to select asset.',
      };
    }
  }

  // Toggle asset visibility
  async toggleAssetVisibility(assetId: string): Promise<ApiResponse<AssetVisibilityResponse>> {
    try {
      const response = await fetch(`${API_BASE_URL}/assets/${assetId}/visibility`, {
        method: 'PUT',
        headers: this.getHeaders(),
      });

      return this.handleResponse<AssetVisibilityResponse>(response);
    } catch (error: any) {
      console.error('Toggle asset visibility error:', error);
      return {
        success: false,
        message: error.message || 'Failed to update asset visibility.',
      };
    }
  }

  // ==================== AUTH ENDPOINTS ====================

  // Signup (Screen 1)
  async signup(data: {
    name: string;
    username: string;
    email: string;
    password: string;
    confirmPassword: string;
    phoneNumber: string;
    country: string;
    state?: string;
    city?: string;
    address: string;
    profilePhoto?: string;
    referralCode?: string;
  }): Promise<ApiResponse<SignupResponse>> {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/signup`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify(data),
      });

      const result = await this.handleResponse<SignupResponse>(response);
      console.log('Signup response:', result);
      if (result.success && result.data?.token) {
        this.setToken(result.data.token);
        // Store token in AsyncStorage for persistence
        await AsyncStorage.setItem('authToken', result.data.token);
        await AsyncStorage.setItem('userId', result.data.userId);
      }

      return result;
    } catch (error: any) {
      console.error('Signup error:', error);
      return {
        success: false,
        message: error.message || 'Network error. Please check your connection.',
      };
    }
  }

  // Login (if needed)
  async login(email: string, password: string): Promise<ApiResponse<SignupResponse>> {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/login`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({ email, password }),
      });

      const result = await this.handleResponse<SignupResponse>(response);
      if (result.success && result.data?.token) {
        this.setToken(result.data.token);
        await AsyncStorage.setItem('authToken', result.data.token);
        await AsyncStorage.setItem('userId', result.data.userId);
        await AsyncStorage.setItem('role', result.data.role);
      }

      return result;
    } catch (error: any) {
      console.error('Login error:', error);
      return {
        success: false,
        message: error.message || 'Network error. Please check your connection.',
      };
    }
  }

  // ==================== UPLOAD ENDPOINTS ====================

  // Upload single image
  async uploadImage(
    imageUri: string,
    type: 'profile' | 'document' | 'facial' = 'document'
  ): Promise<ApiResponse<UploadResponse>> {
    try {
      // Convert image to base64
      const response = await fetch(imageUri);
      const blob = await response.blob();
      const base64 = await this.blobToBase64(blob);

      const uploadResponse = await fetch(`${API_BASE_URL}/upload/base64`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({
          image: base64,
          folder: `signup_app/${type}`,
          fileName: `${type}_${Date.now()}`,
        }),
      });

      return this.handleResponse<UploadResponse>(uploadResponse);
    } catch (error: any) {
      console.error('Upload image error:', error);
      return {
        success: false,
        message: error.message || 'Failed to upload image. Please try again.',
      };
    }
  }

  // Upload multiple images (for facial verification)
  async uploadImages(
    imageUris: string[],
    type: 'document' | 'facial' = 'document'
  ): Promise<ApiResponse<MultipleUploadResponse>> {
    try {
      // Convert all images to base64
      const uploadPromises = imageUris.map(async (uri) => {
        const response = await fetch(uri);
        const blob = await response.blob();
        return this.blobToBase64(blob);
      });

      const base64Images = await Promise.all(uploadPromises);

      const uploadResponse = await fetch(`${API_BASE_URL}/upload/facial`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({
          images: base64Images,
          type,
        }),
      });

      return this.handleResponse<MultipleUploadResponse>(uploadResponse);
    } catch (error: any) {
      console.error('Upload images error:', error);
      return {
        success: false,
        message: error.message || 'Failed to upload images. Please try again.',
      };
    }
  }

  // ==================== SIGNUP FLOW ENDPOINTS ====================

  // Create a KYC session via Smile ID
  async createKYCSession(): Promise<ApiResponse<{
    jobId: string;
    partnerId: string;
    session: Record<string, unknown>;
  }>> {
    const headers = this.getHeaders();
    const response = await fetch(`${API_BASE_URL}/kyc/session`, {
      method: 'POST',
      headers,
    });
    return response.json();
  }

  // Get KYC verification status
  async getKYCStatus(): Promise<ApiResponse<{
    verificationStatus: string;
    verificationNotes: string;
    smileJobId: string;
    lastAction: string;
    lastActionAt: string;
  }>> {
    const headers = this.getHeaders();
    const response = await fetch(`${API_BASE_URL}/kyc/status`, {
      method: 'GET',
      headers,
    });
    return response.json();
  }

  // ==================== USER PROFILE ENDPOINTS ====================

  // Get user profile
  async getProfile(): Promise<ApiResponse<any>> {
    try {
      const response = await fetch(`${API_BASE_URL}/users/profile`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<any>(response);
    } catch (error: any) {
      console.error('Get profile error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch profile.',
      };
    }
  }

  // ==================== UTILITY FUNCTIONS ====================

  // Check if user is authenticated
  async checkAuth(): Promise<boolean> {
    try {
      const token = await AsyncStorage.getItem('authToken');
      const userId = await AsyncStorage.getItem('userId');

      if (token && userId) {
        this.setToken(token);
        return true;
      }
      return false;
    } catch (error) {
      console.error('Check auth error:', error);
      return false;
    }
  }

  // Clear authentication data
  async clearAuth(): Promise<void> {
    try {
      this.clearToken();
      await AsyncStorage.removeItem('authToken');
      await AsyncStorage.removeItem('userId');
      await AsyncStorage.removeItem('currentSignupStep');
      await AsyncStorage.removeItem('signupData');
    } catch (error) {
      console.error('Clear auth error:', error);
    }
  }

  // Get stored user ID
  async getStoredUserId(): Promise<string | null> {
    try {
      return await AsyncStorage.getItem('userId');
    } catch (error) {
      console.error('Get stored user ID error:', error);
      return null;
    }
  }

  // Get stored token
  async getStoredToken(): Promise<string | null> {
    this.getHeaders();
    console.log('Getting stored token with headers:', this.getHeaders());
    try {
      return await AsyncStorage.getItem('authToken');
    } catch (error) {
      console.error('Get stored token error:', error);
      return null;
    }
  }

  // ==================== ERROR HANDLING ====================

  // Handle network errors
  private isNetworkError(error: any): boolean {
    return (
      error.message?.includes('Network request failed') ||
      error.message?.includes('Failed to fetch') ||
      error.message?.includes('Network Error')
    );
  }

  // Get user-friendly error message
  getErrorMessage(error: any): string {
    if (this.isNetworkError(error)) {
      return 'Network error. Please check your internet connection and try again.';
    }

    if (error.message?.includes('timeout')) {
      return 'Request timeout. Please try again.';
    }

    if (error.message?.includes('401') || error.message?.includes('token')) {
      return 'Session expired. Please login again.';
    }

    return error.message || 'An unexpected error occurred. Please try again.';
  }
  async sendOtp(): Promise<ApiResponse<any>> {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/send-otp`, {
        method: 'POST',
        headers: this.getHeaders(),
      });
      return this.handleResponse<any>(response);
    } catch (error: any) {
      console.error('Send OTP error:', error);
      return {
        success: false,
        message: error.message || 'Failed to send verification code.',
      };
    }
  }

  async verifyOtp(code: string): Promise<ApiResponse<any>> {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/verify-otp`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({ code }),
      });
      return this.handleResponse<any>(response);
    } catch (error: any) {
      console.error('Verify OTP error:', error);
      return {
        success: false,
        message: error.message || 'Failed to verify code.',
      };
    }
  }

  async resendOtp(): Promise<ApiResponse<any>> {
    try {
      const response = await fetch(`${API_BASE_URL}/auth/resend-otp`, {
        method: 'POST',
        headers: this.getHeaders(),
      });
      return this.handleResponse<any>(response);
    } catch (error: any) {
      console.error('Resend OTP error:', error);
      return {
        success: false,
        message: error.message || 'Failed to resend verification code.',
      };
    }
  }

  async sendVerificationEmail(): Promise<ApiResponse<any>> {
    return this.sendOtp();
  }
  async createProduct(formData: FormData): Promise<ApiResponse<Product>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products`, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${this.token}`,
          // Don't set Content-Type for FormData, let browser set it
        },
        body: formData,
      });

      return this.handleResponse<Product>(response);
    } catch (error: any) {
      console.error('Create product error:', error);
      return {
        success: false,
        message: error.message || 'Failed to create product',
      };
    }
  }

  // Get user products
  async getUserProducts(params?: {
    page?: number;
    limit?: number;
    status?: string;
    category?: string;
    search?: string;
  }): Promise<ApiResponse<{ products: Product[]; pagination: any }>> {
    try {
      const queryParams = new URLSearchParams();
      if (params) {
        Object.entries(params).forEach(([key, value]) => {
          if (value !== undefined && value !== null) {
            queryParams.append(key, value.toString());
          }
        });
      }

      const response = await fetch(`${API_BASE_URL}/products?${queryParams}`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<{ products: Product[]; pagination: any }>(response);
    } catch (error: any) {
      console.error('Get user products error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch products',
      };
    }
  }

  // Get product by ID
  async getProductById(productId: string): Promise<ApiResponse<Product>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/${productId}`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<Product>(response);
    } catch (error: any) {
      console.error('Get product error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch product',
      };
    }
  }

  // Update product
  async updateProduct(
    productId: string,
    formData: FormData
  ): Promise<ApiResponse<Product>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/${productId}`, {
        method: 'PUT',
        headers: {
          'Authorization': `Bearer ${this.token}`,
        },
        body: formData,
      });

      return this.handleResponse<Product>(response);
    } catch (error: any) {
      console.error('Update product error:', error);
      return {
        success: false,
        message: error.message || 'Failed to update product',
      };
    }
  }

  // Delete product
  async deleteProduct(productId: string): Promise<ApiResponse> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/${productId}`, {
        method: 'DELETE',
        headers: this.getHeaders(),
      });

      return this.handleResponse(response);
    } catch (error: any) {
      console.error('Delete product error:', error);
      return {
        success: false,
        message: error.message || 'Failed to delete product',
      };
    }
  }

  // Update product stock
  async updateProductStock(
    productId: string,
    quantity: number
  ): Promise<ApiResponse<{ stock: number }>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/${productId}/stock`, {
        method: 'PATCH',
        headers: this.getHeaders(),
        body: JSON.stringify({ quantity }),
      });

      return this.handleResponse<{ stock: number }>(response);
    } catch (error: any) {
      console.error('Update stock error:', error);
      return {
        success: false,
        message: error.message || 'Failed to update stock',
      };
    }
  }

  // Get product statistics
  async getProductStats(): Promise<ApiResponse<any>> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/stats`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<any>(response);
    } catch (error: any) {
      console.error('Get product stats error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch product statistics',
      };
    }
  }

  // ==================== FEE ENDPOINTS ====================

  // Get fee configuration (public, no auth required)
  async getFeeConfig(): Promise<ApiResponse<{
    merchantFeeBPS: number;
    mcgpMerchantFeeBPS: number;
    buyerCashbackBPS: number;
    uplineFeeBPS: number;
    systemFeeBPS: number;
    merchantFeePercent: number;
    buyerCashbackPercent: number;
    uplineFeePercent: number;
    gasFeeUSD: number;
    buyerPlatformFee: number;
  }>> {
    try {
      const response = await fetch(`${API_BASE_URL}/fees`, {
        method: 'GET',
        headers: { 'Content-Type': 'application/json' },
      });
      return this.handleResponse(response);
    } catch (error: any) {
      return {
        success: false,
        message: error.message || 'Failed to fetch fee configuration',
      };
    }
  }

  // ==================== CATEGORY ENDPOINTS ====================

  // Get categories
  async getCategories(params?: {
    type?: 'Product' | 'Service' | 'Both';
    parent?: string;
    active?: boolean;
  }): Promise<ApiResponse<Category[]>> {
    try {
      const queryParams = new URLSearchParams();
      if (params) {
        Object.entries(params).forEach(([key, value]) => {
          if (value !== undefined && value !== null) {
            queryParams.append(key, value.toString());
          }
        });
      }

      const response = await fetch(`${API_BASE_URL}/products/category/all?${queryParams}`, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<Category[]>(response);
    } catch (error: any) {
      console.error('Get categories error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch categories',
      };
    }
  }

  // Get category tree
  async getCategoryTree(type?: 'Product' | 'Service'): Promise<ApiResponse<Category[]>> {
    try {
      const url = type
        ? `${API_BASE_URL}/products/category/tree?type=${type}`
        : `${API_BASE_URL}/products/category/tree`;

      const response = await fetch(url, {
        method: 'GET',
        headers: this.getHeaders(),
      });

      return this.handleResponse<Category[]>(response);
    } catch (error: any) {
      console.error('Get category tree error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch category tree',
      };
    }
  }
  //Public Endpoints:
  // Get products by category (public - no auth required)
  async getProductsByCategory(params?: {
    categoryId?: string;
    subcategoryId?: string;
    page?: number;
    limit?: number;
    minPrice?: number;
    maxPrice?: number;
    type?: 'Product' | 'Service';
    status?: string;
    search?: string;
    sortBy?: string;
    sortOrder?: 'asc' | 'desc';
  }): Promise<ApiResponse<{ products: Product[]; category: any; pagination: any; filters: any }>> {
    try {
      const queryParams = new URLSearchParams();
      if (params) {
        Object.entries(params).forEach(([key, value]) => {
          if (value !== undefined && value !== null) {
            queryParams.append(key, value.toString());
          }
        });
      }

      const response = await fetch(`${API_BASE_URL}/products/public/category?${queryParams}`, {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json',
        },
      });

      return this.handleResponse<{ products: Product[]; category: any; pagination: any; filters: any }>(response);
    } catch (error: any) {
      console.error('Get products by category error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch products by category',
      };
    }
  }

  // Get products by category tree
  async getProductsByCategoryTree(
    categoryId: string,
    includeSubcategories: boolean = true
  ): Promise<ApiResponse<{ products: Product[]; mainCategory: any; groupedBySubcategory: any; statistics: any }>> {
    try {
      const response = await fetch(
        `${API_BASE_URL}/products/public/category/tree/${categoryId}?includeSubcategories=${includeSubcategories}`,
        {
          method: 'GET',
          headers: {
            'Content-Type': 'application/json',
          },
        }
      );

      return this.handleResponse<{ products: Product[]; mainCategory: any; groupedBySubcategory: any; statistics: any }>(response);
    } catch (error: any) {
      console.error('Get products by category tree error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch products by category tree',
      };
    }
  }

  // Get marketplace products
  async getMarketplaceProducts(params?: {
    category?: string;
    subcategory?: string;
    type?: 'Product' | 'Service';
    location?: string;
    minPrice?: number;
    maxPrice?: number;
    sort?: 'recent' | 'popular' | 'price_low' | 'price_high' | 'rating';
    page?: number;
    limit?: number;
    search?: string;
  }): Promise<ApiResponse<{ products: Product[]; featuredProducts: Product[]; pagination: any; filters: any }>> {
    try {
      const queryParams = new URLSearchParams();
      if (params) {
        Object.entries(params).forEach(([key, value]) => {
          if (value !== undefined && value !== null) {
            queryParams.append(key, value.toString());
          }
        });
      }

      const response = await fetch(`${API_BASE_URL}/products?${queryParams}`, {
        method: 'GET',
        headers: {
          'Content-Type': 'application/json',
        },
      });

      return this.handleResponse<{ products: Product[]; featuredProducts: Product[]; pagination: any; filters: any }>(response);
    } catch (error: any) {
      console.error('Get marketplace products error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch marketplace products',
      };
    }
  }

  // ==================== FILE UPLOAD HELPER ====================

  // Convert image to FormData for upload
  createProductFormData(data: {
    images: string[];
    name: string;
    description: string;
    price: number;
    stock: number;
    category?: string;
    location: string;
    phoneNumber: string;
    email: string;
    companyName?: string;
    attributes?: Array<{ name: string; value: string }>;
    type?: 'Product' | 'Service';
  }): FormData {
    const formData = new FormData();

    // Add images
    data.images.forEach((uri, index) => {
      const filename = uri.split('/').pop() || `image_${index}.jpg`;
      const match = /\.(\w+)$/.exec(filename);
      const type = match ? `image/${match[1]}` : 'image/jpeg';

      // @ts-ignore - React Native specific
      formData.append('images', {
        uri,
        name: filename,
        type,
      });
    });

    // Add other fields
    formData.append('name', data.name);
    formData.append('description', data.description);
    formData.append('price', data.price.toString());
    formData.append('stock', data.stock.toString());
    formData.append('location', data.location);
    formData.append('phoneNumber', data.phoneNumber);
    formData.append('email', data.email);

    if (data.category) {
      formData.append('category', data.category);
    }

    if (data.companyName) {
      formData.append('companyName', data.companyName);
    }

    if (data.type) {
      formData.append('type', data.type);
    }

    if (data.attributes && data.attributes.length > 0) {
      formData.append('attributes', JSON.stringify(data.attributes));
    }

    return formData;
  }

  // Upload single image (for testing)
  async uploadProductImage(imageUri: string): Promise<ApiResponse<{ url: string }>> {
    try {
      // Convert image to base64 for testing
      const response = await fetch(imageUri);
      const blob = await response.blob();
      const base64 = await this.blobToBase64(blob);

      const uploadResponse = await fetch(`${API_BASE_URL}/upload/base64`, {
        method: 'POST',
        headers: this.getHeaders(),
        body: JSON.stringify({
          image: base64,
          folder: 'products',
          fileName: `product_${Date.now()}`,
        }),
      });

      return this.handleResponse<{ url: string }>(uploadResponse);
    } catch (error: any) {
      console.error('Upload product image error:', error);
      return {
        success: false,
        message: error.message || 'Failed to upload image',
      };
    }
  }
  //Category
  // Create category (with optional image upload)
  async createCategory(
    categoryData: CreateCategoryRequest | FormData
  ): Promise<ApiResponse<Category>> {
    try {
      let headers = this.getHeaders();
      let body: any = categoryData;

      // Handle FormData (with image)
      if (categoryData instanceof FormData) {
        headers = {
          'Authorization': `Bearer ${this.token}`,
          // Let browser set Content-Type for FormData
        };
      } else {
        // Handle JSON data
        body = JSON.stringify(categoryData);
      }

      const response = await fetch(`${API_BASE_URL}/products/category`, {
        method: 'POST',
        headers,
        body,
      });

      const result = await this.handleResponse<{ data: Category }>(response);

      return {
        success: result.success,
        message: result.message || 'Category created successfully',
        data: result.data?.data,
      };
    } catch (error: any) {
      console.error('Create category error:', error);
      return {
        success: false,
        message: error.message || 'Failed to create category',
      };
    }
  }

  // Update category
  async updateCategory(
    categoryId: string,
    categoryData: UpdateCategoryRequest | FormData
  ): Promise<ApiResponse<Category>> {
    try {
      let headers = this.getHeaders();
      let body: any = categoryData;

      // Handle FormData (with image)
      if (categoryData instanceof FormData) {
        headers = {
          'Authorization': `Bearer ${this.token}`,
          // Let browser set Content-Type for FormData
        };
      } else {
        // Handle JSON data
        body = JSON.stringify(categoryData);
      }

      const response = await fetch(
        `${API_BASE_URL}/products/category/${categoryId}`,
        {
          method: 'PUT',
          headers,
          body,
        }
      );

      const result = await this.handleResponse<{ data: Category }>(response);

      return {
        success: result.success,
        message: result.message || 'Category updated successfully',
        data: result.data?.data,
      };
    } catch (error: any) {
      console.error('Update category error:', error);
      return {
        success: false,
        message: error.message || 'Failed to update category',
      };
    }
  }

  // Delete category
  async deleteCategory(categoryId: string): Promise<ApiResponse> {
    try {
      const response = await fetch(
        `${API_BASE_URL}/products/category/${categoryId}`,
        {
          method: 'DELETE',
          headers: this.getHeaders(),
        }
      );

      return this.handleResponse(response);
    } catch (error: any) {
      console.error('Delete category error:', error);
      return {
        success: false,
        message: error.message || 'Failed to delete category',
      };
    }
  }

  // Get parent categories (no parent)
  async getParentCategories(type?: 'Product' | 'Service' | 'Both'): Promise<ApiResponse<Category[]>> {
    try {
      const params: any = { parent: 'null', active: true };
      if (type) params.type = type;

      return this.getCategories(params);
    } catch (error: any) {
      console.error('Get parent categories error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch parent categories',
        data: [],
      };
    }
  }

  // Get subcategories by parent ID
  async getSubcategories(parentId: string, active: boolean = true): Promise<ApiResponse<Category[]>> {
    try {
      return this.getCategories({
        parent: parentId,
        active,
      });
    } catch (error: any) {
      console.error('Get subcategories error:', error);
      return {
        success: false,
        message: error.message || 'Failed to fetch subcategories',
        data: [],
      };
    }
  }

  // Upload category image
  async uploadCategoryImage(imageUri: string, categoryId?: string): Promise<ApiResponse<{ url: string }>> {
    try {
      // Convert image to FormData
      const formData = new FormData();
      const filename = imageUri.split('/').pop() || `category_${Date.now()}.jpg`;
      const match = /\.(\w+)$/.exec(filename);
      const type = match ? `image/${match[1]}` : 'image/jpeg';

      // @ts-ignore - React Native specific
      formData.append('image', {
        uri: imageUri,
        name: filename,
        type,
      });

      if (categoryId) {
        formData.append('categoryId', categoryId);
      }

      const response = await fetch(`${API_BASE_URL}/upload/category-image`, {
        method: 'POST',
        headers: {
          'Authorization': `Bearer ${this.token}`,
        },
        body: formData,
      });

      return this.handleResponse<{ url: string }>(response);
    } catch (error: any) {
      console.error('Upload category image error:', error);
      return {
        success: false,
        message: error.message || 'Failed to upload category image',
      };
    }
  }

  // Create FormData for category with image
  createCategoryFormData(data: {
    title: string;
    description?: string;
    type: 'Product' | 'Service' | 'Both';
    parentCategory?: string;
    icon?: string;
    color?: string;
    order?: number;
    isActive?: boolean;
    imageUri?: string;
  }): FormData {
    const formData = new FormData();

    // Add image if provided
    if (data.imageUri) {
      const filename = data.imageUri.split('/').pop() || `category_${Date.now()}.jpg`;
      const match = /\.(\w+)$/.exec(filename);
      const type = match ? `image/${match[1]}` : 'image/jpeg';

      // @ts-ignore - React Native specific
      formData.append('image', {
        uri: data.imageUri,
        name: filename,
        type,
      });
    }

    // Add other fields
    formData.append('title', data.title);

    if (data.description) {
      formData.append('description', data.description);
    }

    formData.append('type', data.type);

    if (data.parentCategory) {
      formData.append('parentCategory', data.parentCategory);
    }

    if (data.icon) {
      formData.append('icon', data.icon);
    }

    if (data.color) {
      formData.append('color', data.color);
    }

    if (data.order !== undefined) {
      formData.append('order', data.order.toString());
    }

    if (data.isActive !== undefined) {
      formData.append('isActive', data.isActive.toString());
    }

    return formData;
  }

  // Search categories
  async searchCategories(query: string, type?: 'Product' | 'Service'): Promise<ApiResponse<Category[]>> {
    try {
      const params: any = { search: query, active: true };
      if (type) params.type = type;

      return this.getCategories(params);
    } catch (error: any) {
      console.error('Search categories error:', error);
      return {
        success: false,
        message: error.message || 'Failed to search categories',
        data: [],
      };
    }
  }
  //toggle Featured

  // Bulk update categories (reorder, activate/deactivate)
  async bulkUpdateCategories(
    updates: Array<{
      id: string;
      order?: number;
      isActive?: boolean;
    }>
  ): Promise<ApiResponse> {
    try {
      const response = await fetch(`${API_BASE_URL}/products/category/bulk-update`, {
        method: 'PUT',
        headers: this.getHeaders(),
        body: JSON.stringify({ updates }),
      });

      return this.handleResponse(response);
    } catch (error: any) {
      console.error('Bulk update categories error:', error);
      return {
        success: false,
        message: error.message || 'Failed to update categories',
      };
    }
  }

  // ── Notification Methods ──────────────────────────────────────────

  async getNotifications(page: number = 1, filter?: 'read' | 'unread'): Promise<ApiResponse> {
    const params = new URLSearchParams({ page: String(page), limit: '20' });
    if (filter) params.append('filter', filter);
    const response = await fetch(`${API_BASE_URL}/notifications?${params}`, {
      headers: this.getHeaders(),
    });
    return response.json();
  }

  async markAsRead(id: string): Promise<ApiResponse> {
    const response = await fetch(`${API_BASE_URL}/notifications/${id}/read`, {
      method: 'PATCH',
      headers: this.getHeaders(),
    });
    return response.json();
  }

  async markAllAsRead(): Promise<ApiResponse> {
    const response = await fetch(`${API_BASE_URL}/notifications/read-all`, {
      method: 'PATCH',
      headers: this.getHeaders(),
    });
    return response.json();
  }

  async getUnreadCount(): Promise<ApiResponse> {
    const response = await fetch(`${API_BASE_URL}/notifications/unread-count`, {
      headers: this.getHeaders(),
    });
    return response.json();
  }

  async getNotificationPreferences(): Promise<ApiResponse> {
    const response = await fetch(`${API_BASE_URL}/notifications/preferences`, {
      headers: this.getHeaders(),
    });
    return response.json();
  }

  async updateNotificationPreferences(prefs: Partial<NotificationPreferences>): Promise<ApiResponse> {
    const response = await fetch(`${API_BASE_URL}/notifications/preferences`, {
      method: 'PATCH',
      headers: this.getHeaders('application/json'),
      body: JSON.stringify(prefs),
    });
    return response.json();
  }
}
// Category methods
export interface Category {
  id: string;
  _id?: string;
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string | Category;
  icon?: string;
  color: string;
  isActive: boolean;
  order: number;
  productCount: number;
  children?: Category[];
  createdAt?: string;
  updatedAt?: string;
}

// Category Tree Node interface
export interface CategoryTreeNode extends Category {
  children: CategoryTreeNode[];
}

// Create Category Request interface
export interface CreateCategoryRequest {
  title: string;
  description?: string;
  type: 'Product' | 'Service' | 'Both';
  parentCategory?: string;
  icon?: string;
  color?: string;
  order?: number;
  isActive?: boolean;
  image?: any; // For FormData upload
}

// Update Category Request interface
export interface UpdateCategoryRequest {
  title?: string;
  description?: string;
  type?: 'Product' | 'Service' | 'Both';
  parentCategory?: string | null;
  icon?: string;
  color?: string;
  order?: number;
  isActive?: boolean;
  image?: any; // For FormData upload
}

// Category Response interface
export interface CategoryResponse {
  success: boolean;
  message: string;
  data: Category | Category[];
  pagination?: {
    page: number;
    limit: number;
    total: number;
    pages: number;
  };
}

// Category Tree Response interface
export interface CategoryTreeResponse {
  success: boolean;
  data: CategoryTreeNode[];
}

// Category with Details interface
export interface CategoryWithDetails extends Category {
  parent?: Category;
  children: Category[];
  productCount: number;
}

export async function getMyMerchantRequest(): Promise<ApiResponse> {
  try {
    const token = await AsyncStorage.getItem('authToken');
    const response = await fetch(`${API_BASE_URL}/merchant-requests/my-request`, {
      headers: {
        'Authorization': token ? `Bearer ${token}` : '',
        'Content-Type': 'application/json',
      },
    });
    if (!response.ok) {
      const body = await response.json().catch(() => null);
      return { success: false, message: body?.message || `Server error (${response.status})` };
    }
    return response.json();
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to fetch merchant request' };
  }
}

export async function submitMerchantRequest(data: {
  businessType: string;
  businessName: string;
  businessDescription?: string;
  address: string;
  city: string;
  state: string;
  country: string;
  phone: string;
  registrationNumber?: string;
}): Promise<ApiResponse> {
  try {
    const token = await AsyncStorage.getItem('authToken');
    const response = await fetch(`${API_BASE_URL}/merchant-requests`, {
      method: 'POST',
      headers: {
        'Authorization': token ? `Bearer ${token}` : '',
        'Content-Type': 'application/json',
      },
      body: JSON.stringify(data),
    });
    if (!response.ok) {
      const body = await response.json().catch(() => null);
      return { success: false, message: body?.message || `Server error (${response.status})` };
    }
    return response.json();
  } catch (error: any) {
    return { success: false, message: error.message || 'Failed to submit merchant request' };
  }
}

// Create singleton instance
export const api = new APIService();

// Optional: Create a hook version for React components
export const useApi = () => {
  return api;
};

export default api;