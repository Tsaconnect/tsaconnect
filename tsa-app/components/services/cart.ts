// components/services/cart.ts
import AsyncStorage from '@react-native-async-storage/async-storage';

import { API_BASE_URL } from '../../constants/api/config';

// Types based on your backend response structure
export interface ProductImage {
    url: string;
    publicId?: string;
    isPrimary?: boolean;
}

export interface Product {
    _id: string;
    name: string;
    description: string;
    price: number;
    stock: number;
    images: ProductImage[];
    category?: {
        _id?: string;
        title?: string;
        color?: string;
    };
    categoryName?: string;
    attributes?: Array<{
        name: string;
        value: string;
    }>;
    rating?: {
        average: number;
        count: number;
    };
    status?: 'active' | 'inactive' | 'draft';
    views?: number;
    location?: string;
    phoneNumber?: string;
    companyName?: string;
    email?: string;
    userId?: string;
    createdAt: string;
    updatedAt: string;
}

export interface Seller {
    _id: string;
    name: string;
    email: string;
    companyName?: string;
}

export interface CartItem {
    _id: string;
    product: Product | string;
    seller: Seller;
    quantity: number;
    price: number;
    selectedAttributes?: Array<{
        name: string;
        value: string;
    }>;
    notes?: string;
    addedAt: string;
    updatedAt: string;
}

export interface CartSummary {
    totalItems: number;
    totalQuantity: number;
    subtotal: number;
    shipping: number;
    gasFee: number;
    platformFee: number;
    discount: number;
    total: number;
}

export interface FeeConfig {
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
}

export interface ShippingAddress {
    name: string;
    phoneNumber: string;
    address: string;
    city: string;
    state?: string;
    country: string;
    postalCode?: string;
    isDefault?: boolean;
}

export interface BillingAddress extends ShippingAddress {
    sameAsShipping?: boolean;
}

export interface ShippingMethod {
    method: 'standard' | 'express' | 'next_day' | 'pickup';
    provider?: string;
    estimatedDelivery?: string;
    cost?: number;
}

export interface PaymentMethod {
    method: 'card' | 'bank_transfer' | 'wallet' | 'cash_on_delivery' | 'crypto';
    details?: any;
}

export interface Coupon {
    code: string;
    discountType: 'percentage' | 'fixed';
    discountValue: number;
    maxDiscount?: number;
    minPurchase?: number;
    expiresAt?: string;
}

export interface Cart {
    _id: string;
    user: string;
    items: CartItem[];
    summary: CartSummary;
    status: 'active' | 'abandoned' | 'converted' | 'expired';
    appliedCoupon?: Coupon;
    shippingAddress?: ShippingAddress;
    billingAddress?: BillingAddress;
    shippingMethod?: ShippingMethod;
    paymentMethod?: PaymentMethod;
    estimatedDelivery?: string;
    lastActivity: string;
    expiresAt: string;
    createdAt: string;
    updatedAt: string;
}

export interface AddToCartRequest {
    productId: string;
    quantity?: number;
    selectedAttributes?: Array<{ name: string; value: string }>;
    notes?: string;
}

export interface UpdateCartItemRequest {
    quantity: number;
}

export interface ApplyCouponRequest {
    couponCode: string;
}

export interface ValidationIssue {
    productId: string;
    productName: string;
    issue: string;
    available?: number;
    requested?: number;
    status?: string;
    oldPrice?: number;
    newPrice?: number;
}

export interface ValidationResult {
    productId: string;
    name: string;
    requestedQuantity: number;
    availableStock: number;
    price: number;
    currentPrice: number;
    isAvailable: boolean;
    issues: string[];
    isValid: boolean;
}

export interface CartValidationResult {
    valid: boolean;
    validationResults: ValidationResult[];
    issues: ValidationIssue[];
    cart?: Cart;
}

export interface ItemsBySeller {
    seller: Seller;
    items: CartItem[];
    subtotal: number;
}

export interface CartSummaryResponse {
    cart: Cart;
    itemsBySeller: ItemsBySeller[];
    summary: CartSummary;
    feeConfig?: FeeConfig;
    appliedCoupon?: Coupon;
    shippingAddress?: ShippingAddress;
    billingAddress?: BillingAddress;
    paymentMethod?: PaymentMethod;
    shippingMethod?: ShippingMethod;
}

export interface CheckoutResponse {
    orderId: string;
    cartId: string;
    summary: CartSummary;
    items: CartItem[];
    shippingAddress?: ShippingAddress;
    billingAddress?: BillingAddress;
    paymentMethod?: PaymentMethod;
    shippingMethod?: ShippingMethod;
    estimatedDelivery?: string;
    createdAt: string;
}

export interface ApiResponse<T> {
    success: boolean;
    message?: string;
    data?: T;
    errors?: any[];
}

export interface PaginationInfo {
    page: number;
    limit: number;
    total: number;
    totalPages: number;
    hasNextPage: boolean;
    hasPrevPage: boolean;
}

export interface AbandonedCartsResponse {
    carts: Cart[];
    pagination: PaginationInfo;
    filters: {
        days: number;
        cutoffDate: string;
    };
}

export interface CleanupResponse {
    deletedCount: number;
    timestamp: string;
}

class CartService {
    private token: string | null = null;

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

    // Set token for authenticated requests
    setToken(token: string) {
        this.token = token;
    }

    // Clear token
    clearToken() {
        this.token = null;
    }

    // Get token from storage
    async initializeToken() {
        try {
            const token = await AsyncStorage.getItem('authToken');
            if (token) {
                this.setToken(token);
            }
        } catch (error) {
            console.error('Error initializing token:', error);
        }
    }

    // Get headers for requests
    private getHeaders(contentType: string = 'application/json'): HeadersInit {
        const headers: HeadersInit = {
            'Content-Type': contentType,
        };

        if (this.token) {
            headers['Authorization'] = this.token.startsWith('Bearer ') ? this.token : `Bearer ${this.token}`;
        }

        return headers;
    }

    // ==================== PRODUCT ENDPOINTS ====================

    /**
     * Get product by ID
     */
    async getProductById(productId: string): Promise<ApiResponse<Product>> {
        try {
            const response = await fetch(`${API_BASE_URL}/products/${productId}`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Product>(response);
        } catch (error: any) {
            console.error('Get product by ID error:', error);
            return {
                success: false,
                message: error.message || 'Failed to fetch product details',
            };
        }
    }

    // ==================== CART ENDPOINTS ====================

    /**
     * Get or create cart
     */
    async getOrCreateCart(): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Get or create cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to retrieve cart',
            };
        }
    }

    /**
     * Add item to cart
     */
    async addToCart(data: AddToCartRequest): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/items`, {
                method: 'POST',
                headers: this.getHeaders(),
                body: JSON.stringify(data),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Add to cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to add item to cart',
            };
        }
    }

    /**
     * Update cart item quantity
     */
    async updateCartItem(itemId: string, data: UpdateCartItemRequest): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/items/${itemId}`, {
                method: 'PUT',
                headers: this.getHeaders(),
                body: JSON.stringify(data),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Update cart item error:', error);
            return {
                success: false,
                message: error.message || 'Failed to update cart item',
            };
        }
    }

    /**
     * Remove item from cart
     */
    async removeFromCart(itemId: string): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/items/${itemId}`, {
                method: 'DELETE',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Remove from cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to remove item from cart',
            };
        }
    }

    /**
     * Clear cart
     */
    async clearCart(): Promise<ApiResponse<{ items: []; summary: CartSummary }>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/clear`, {
                method: 'DELETE',
                headers: this.getHeaders(),
            });

            return this.handleResponse<{ items: []; summary: CartSummary }>(response);
        } catch (error: any) {
            console.error('Clear cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to clear cart',
            };
        }
    }

    /**
     * Apply coupon to cart
     */
    async applyCoupon(data: ApplyCouponRequest): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/coupon`, {
                method: 'POST',
                headers: this.getHeaders(),
                body: JSON.stringify(data),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Apply coupon error:', error);
            return {
                success: false,
                message: error.message || 'Failed to apply coupon',
            };
        }
    }

    /**
     * Remove coupon from cart
     */
    async removeCoupon(): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/coupon`, {
                method: 'DELETE',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Remove coupon error:', error);
            return {
                success: false,
                message: error.message || 'Failed to remove coupon',
            };
        }
    }

    /**
     * Update shipping address
     */
    async updateShippingAddress(addressData: ShippingAddress): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/shipping-address`, {
                method: 'PUT',
                headers: this.getHeaders(),
                body: JSON.stringify(addressData),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Update shipping address error:', error);
            return {
                success: false,
                message: error.message || 'Failed to update shipping address',
            };
        }
    }

    /**
     * Update billing address
     */
    async updateBillingAddress(addressData: BillingAddress): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/billing-address`, {
                method: 'PUT',
                headers: this.getHeaders(),
                body: JSON.stringify(addressData),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Update billing address error:', error);
            return {
                success: false,
                message: error.message || 'Failed to update billing address',
            };
        }
    }

    /**
     * Update shipping method
     */
    async updateShippingMethod(data: {
        method: 'standard' | 'express' | 'next_day' | 'pickup';
        provider?: string;
        estimatedDelivery?: string;
    }): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/shipping-method`, {
                method: 'PUT',
                headers: this.getHeaders(),
                body: JSON.stringify(data),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Update shipping method error:', error);
            return {
                success: false,
                message: error.message || 'Failed to update shipping method',
            };
        }
    }

    /**
     * Update payment method
     */
    async updatePaymentMethod(data: {
        method: 'card' | 'bank_transfer' | 'wallet' | 'cash_on_delivery' | 'crypto';
        details?: any;
    }): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/payment-method`, {
                method: 'PUT',
                headers: this.getHeaders(),
                body: JSON.stringify(data),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Update payment method error:', error);
            return {
                success: false,
                message: error.message || 'Failed to update payment method',
            };
        }
    }

    /**
     * Convert cart to order (checkout)
     */
    async checkout(): Promise<ApiResponse<CheckoutResponse>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/checkout`, {
                method: 'POST',
                headers: this.getHeaders(),
            });

            return this.handleResponse<CheckoutResponse>(response);
        } catch (error: any) {
            console.error('Checkout error:', error);
            return {
                success: false,
                message: error.message || 'Failed to process checkout',
            };
        }
    }

    /**
     * Get cart summary with items grouped by seller
     */
    async getCartSummary(): Promise<ApiResponse<CartSummaryResponse>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/summary`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<CartSummaryResponse>(response);
        } catch (error: any) {
            console.error('Get cart summary error:', error);
            return {
                success: false,
                message: error.message || 'Failed to get cart summary',
            };
        }
    }

    /**
     * Validate cart items (check stock and availability)
     */
    async validateCart(): Promise<ApiResponse<CartValidationResult>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/validate`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<CartValidationResult>(response);
        } catch (error: any) {
            console.error('Validate cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to validate cart',
            };
        }
    }

    /**
     * Restore abandoned cart
     */
    async restoreCart(cartId: string): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/restore/${cartId}`, {
                method: 'POST',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Restore cart error:', error);
            return {
                success: false,
                message: error.message || 'Failed to restore cart',
            };
        }
    }

    // ==================== ADMIN CART ENDPOINTS ====================

    /**
     * Get abandoned carts (admin only)
     */
    async getAbandonedCarts(params?: {
        days?: number;
        page?: number;
        limit?: number;
    }): Promise<ApiResponse<AbandonedCartsResponse>> {
        try {
            const queryParams = new URLSearchParams();
            if (params) {
                Object.entries(params).forEach(([key, value]) => {
                    if (value !== undefined && value !== null) {
                        queryParams.append(key, value.toString());
                    }
                });
            }

            const response = await fetch(`${API_BASE_URL}/cart/admin/abandoned?${queryParams}`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<AbandonedCartsResponse>(response);
        } catch (error: any) {
            console.error('Get abandoned carts error:', error);
            return {
                success: false,
                message: error.message || 'Failed to fetch abandoned carts',
            };
        }
    }

    /**
     * Get cart by ID (admin only)
     */
    async getCartById(cartId: string): Promise<ApiResponse<Cart>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/admin/${cartId}`, {
                method: 'GET',
                headers: this.getHeaders(),
            });

            return this.handleResponse<Cart>(response);
        } catch (error: any) {
            console.error('Get cart by ID error:', error);
            return {
                success: false,
                message: error.message || 'Failed to fetch cart',
            };
        }
    }

    /**
     * Clean up expired carts (admin only)
     */
    async cleanupExpiredCarts(): Promise<ApiResponse<CleanupResponse>> {
        try {
            const response = await fetch(`${API_BASE_URL}/cart/admin/cleanup`, {
                method: 'DELETE',
                headers: this.getHeaders(),
            });

            return this.handleResponse<CleanupResponse>(response);
        } catch (error: any) {
            console.error('Cleanup expired carts error:', error);
            return {
                success: false,
                message: error.message || 'Failed to cleanup expired carts',
            };
        }
    }

    // ==================== CART UTILITY FUNCTIONS ====================

    /**
     * Calculate total items in cart
     */
    calculateTotalItems(cart: Cart | null): number {
        if (!cart || !cart.items) return 0;
        return cart.items.reduce((total, item) => total + item.quantity, 0);
    }

    /**
     * Calculate total price of items in cart
     */
    calculateSubtotal(cart: Cart | null): number {
        if (!cart || !cart.items) return 0;
        return cart.items.reduce((total, item) => total + (item.price * item.quantity), 0);
    }

    /**
     * Check if cart is empty
     */
    isCartEmpty(cart: Cart | null): boolean {
        return !cart || !cart.items || cart.items.length === 0;
    }

    /**
     * Group cart items by seller
     */
    groupItemsBySeller(cart: Cart | null): ItemsBySeller[] {
        if (!cart || !cart.items) return [];

        const itemsBySeller: { [key: string]: ItemsBySeller } = {};

        cart.items.forEach(item => {
            const sellerId = item.seller._id.toString();
            if (!itemsBySeller[sellerId]) {
                itemsBySeller[sellerId] = {
                    seller: item.seller,
                    items: [],
                    subtotal: 0
                };
            }
            itemsBySeller[sellerId].items.push(item);
            itemsBySeller[sellerId].subtotal += item.price * item.quantity;
        });

        return Object.values(itemsBySeller);
    }

    /**
     * Get cart item count
     */
    async getCartItemCount(): Promise<number> {
        try {
            const response = await this.getCartSummary();
            if (response.success && response.data) {
                return response.data.summary.totalQuantity || 0;
            }
            return 0;
        } catch (error) {
            console.error('Get cart item count error:', error);
            return 0;
        }
    }

    /**
     * Format price with currency symbol
     */
    formatPrice(price: number): string {
        return `$${price.toLocaleString()}`;
    }
}

// Create and export a single instance
export const cartService = new CartService();

// Also export the class for testing or custom instances
export default cartService;