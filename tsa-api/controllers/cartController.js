// controllers/CartController.js
const Cart = require('../models/Cart');
const Product = require('../models/Product');
// const mongoose = require('mongoose');

class CartController {
    // Create new cart or get existing cart
    async getOrCreateCart(req, res) {
        try {
            const userId = req.user._id;

            // Check if user already has an active cart
            let cart = await Cart.findOne({
                user: userId,
                status: 'active'
            })

            // If no active cart exists, create one
            if (!cart) {
                cart = await Cart.create({
                    user: userId,
                    items: [],
                    summary: {
                        totalItems: 0,
                        totalQuantity: 0,
                        subtotal: 0,
                        shipping: 0,
                        tax: 0,
                        discount: 0,
                        total: 0
                    }
                });
            }

            res.json({
                success: true,
                message: 'Cart retrieved successfully',
                data: cart
            });
        } catch (error) {
            console.error('Get or create cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to get cart'
            });
        }
    }

    // Add item to cart
    async addToCart(req, res) {
        try {
            const userId = req.user._id;
            const {
                productId,
                quantity = 1,
                selectedAttributes = [],
                notes
            } = req.body;

            // Validate input
            if (!productId) {
                return res.status(400).json({
                    success: false,
                    message: 'Product ID is required'
                });
            }

            // Get product details
            const product = await Product.findById(productId)
                .populate('userId', 'name email companyName');

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Check product availability
            if (product.stock < quantity) {
                return res.status(400).json({
                    success: false,
                    message: `Only ${product.stock} items available in stock`
                });
            }

            // Check product status
            if (product.status !== 'active') {
                return res.status(400).json({
                    success: false,
                    message: 'Product is not available for purchase'
                });
            }

            // Get or create user's cart
            let cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                cart = await Cart.create({
                    user: userId,
                    items: [],
                    summary: {
                        totalItems: 0,
                        totalQuantity: 0,
                        subtotal: 0,
                        shipping: 0,
                        tax: 0,
                        discount: 0,
                        total: 0
                    }
                });
            }

            // Add item to cart
            await cart.addItem({
                product: productId,
                seller: product.userId._id,
                quantity: parseInt(quantity),
                price: product.price,
                selectedAttributes,
                notes
            });

            // Reload cart with populated data
            cart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Item added to cart successfully',
                data: cart
            });
        } catch (error) {
            console.error('Add to cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to add item to cart'
            });
        }
    }

    // Update item quantity
    async updateCartItem(req, res) {
        try {
            const userId = req.user._id;
            const { itemId } = req.params;
            const { quantity } = req.body;

            if (!quantity || quantity < 0) {
                return res.status(400).json({
                    success: false,
                    message: 'Valid quantity is required'
                });
            }

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Find the item in cart
            const cartItem = cart.items.find(item => item._id.toString() === itemId);
            if (!cartItem) {
                return res.status(404).json({
                    success: false,
                    message: 'Item not found in cart'
                });
            }

            // Get product to check stock
            const product = await Product.findById(cartItem.product);
            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Check stock availability
            if (quantity > product.stock) {
                return res.status(400).json({
                    success: false,
                    message: `Only ${product.stock} items available in stock`
                });
            }

            // Update quantity
            await cart.updateItemQuantity(itemId, parseInt(quantity));

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Cart updated successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Update cart item error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to update cart item'
            });
        }
    }

    // Remove item from cart
    async removeFromCart(req, res) {
        try {
            const userId = req.user._id;
            const { itemId } = req.params;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Remove item
            await cart.removeItem(itemId);

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Item removed from cart successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Remove from cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to remove item from cart'
            });
        }
    }

    // Clear cart
    async clearCart(req, res) {
        try {
            const userId = req.user._id;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Clear cart
            await cart.clearCart();

            res.json({
                success: true,
                message: 'Cart cleared successfully',
                data: {
                    items: [],
                    summary: {
                        totalItems: 0,
                        totalQuantity: 0,
                        subtotal: 0,
                        shipping: 0,
                        tax: 0,
                        discount: 0,
                        total: 0
                    }
                }
            });
        } catch (error) {
            console.error('Clear cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to clear cart'
            });
        }
    }

    // Apply coupon
    async applyCoupon(req, res) {
        try {
            const userId = req.user._id;
            const { couponCode } = req.body;

            if (!couponCode) {
                return res.status(400).json({
                    success: false,
                    message: 'Coupon code is required'
                });
            }

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Check if cart is empty
            if (cart.isEmpty()) {
                return res.status(400).json({
                    success: false,
                    message: 'Cannot apply coupon to empty cart'
                });
            }

            // In a real application, you would validate the coupon from database
            // For now, we'll use a mock coupon validation
            const couponData = {
                code: couponCode,
                discountType: 'percentage',
                discountValue: 10, // 10% discount
                maxDiscount: 50, // Maximum $50 discount
                minPurchase: 100, // Minimum $100 purchase required
                expiresAt: new Date(Date.now() + 30 * 24 * 60 * 60 * 1000) // 30 days from now
            };

            // Validate coupon requirements
            if (cart.summary.subtotal < couponData.minPurchase) {
                return res.status(400).json({
                    success: false,
                    message: `Minimum purchase of $${couponData.minPurchase} required for this coupon`
                });
            }

            // Apply coupon
            await cart.applyCoupon(couponCode, couponData);

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Coupon applied successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Apply coupon error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to apply coupon'
            });
        }
    }

    // Remove coupon
    async removeCoupon(req, res) {
        try {
            const userId = req.user._id;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Remove coupon
            await cart.removeCoupon();

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Coupon removed successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Remove coupon error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to remove coupon'
            });
        }
    }

    // Update shipping address
    async updateShippingAddress(req, res) {
        try {
            const userId = req.user._id;
            const addressData = req.body;

            // Validate required fields
            if (!addressData.name || !addressData.phoneNumber || !addressData.address || !addressData.city || !addressData.country) {
                return res.status(400).json({
                    success: false,
                    message: 'Missing required address fields'
                });
            }

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Update shipping address
            await cart.updateShippingAddress(addressData);

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Shipping address updated successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Update shipping address error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to update shipping address'
            });
        }
    }

    // Update billing address
    async updateBillingAddress(req, res) {
        try {
            const userId = req.user._id;
            const addressData = req.body;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Update billing address
            await cart.updateBillingAddress(addressData);

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Billing address updated successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Update billing address error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to update billing address'
            });
        }
    }

    // Update shipping method
    async updateShippingMethod(req, res) {
        try {
            const userId = req.user._id;
            const { method, provider, estimatedDelivery } = req.body;

            // Validate shipping method
            const validMethods = ['standard', 'express', 'next_day', 'pickup'];
            if (!method || !validMethods.includes(method)) {
                return res.status(400).json({
                    success: false,
                    message: 'Valid shipping method is required'
                });
            }

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Update shipping method
            await cart.updateShippingMethod({
                method,
                provider,
                estimatedDelivery
            });

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Shipping method updated successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Update shipping method error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to update shipping method'
            });
        }
    }

    // Update payment method
    async updatePaymentMethod(req, res) {
        try {
            const userId = req.user._id;
            const { method, details } = req.body;

            // Validate payment method
            const validMethods = ['card', 'bank_transfer', 'wallet', 'cash_on_delivery', 'crypto'];
            if (!method || !validMethods.includes(method)) {
                return res.status(400).json({
                    success: false,
                    message: 'Valid payment method is required'
                });
            }

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Update payment method
            await cart.updatePaymentMethod({
                method,
                details
            });

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
                .populate('items.product', 'name images price stock rating')
                .populate('items.seller', 'name email companyName');

            res.json({
                success: true,
                message: 'Payment method updated successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Update payment method error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to update payment method'
            });
        }
    }

    // Convert cart to order
    async convertToOrder(req, res) {
        try {
            const userId = req.user._id;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            })
            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Validate cart
            if (cart.isEmpty()) {
                return res.status(400).json({
                    success: false,
                    message: 'Cannot convert empty cart to order'
                });
            }

            if (!cart.shippingAddress || !cart.shippingAddress.address) {
                return res.status(400).json({
                    success: false,
                    message: 'Shipping address is required'
                });
            }

            if (!cart.paymentMethod) {
                return res.status(400).json({
                    success: false,
                    message: 'Payment method is required'
                });
            }

            // Check product availability before conversion
            for (const item of cart.items) {
                const product = await Product.findById(item.product);
                if (!product) {
                    return res.status(400).json({
                        success: false,
                        message: `Product "${item.product.name}" is no longer available`
                    });
                }

                if (product.stock < item.quantity) {
                    return res.status(400).json({
                        success: false,
                        message: `Only ${product.stock} items available for "${product.name}"`
                    });
                }
            }

            // In a real application, you would create an Order document here
            // For now, we'll just mark the cart as converted
            await cart.convertToOrder();

            res.json({
                success: true,
                message: 'Order created successfully',
                data: {
                    orderId: `ORD-${Date.now()}`,
                    cartId: cart._id,
                    summary: cart.summary,
                    items: cart.items,
                    shippingAddress: cart.shippingAddress,
                    billingAddress: cart.billingAddress,
                    paymentMethod: cart.paymentMethod,
                    shippingMethod: cart.shippingMethod,
                    estimatedDelivery: cart.estimatedDelivery,
                    createdAt: new Date()
                }
            });
        } catch (error) {
            console.error('Convert to order error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to convert cart to order'
            });
        }
    }

    // Get cart summary
    async getCartSummary(req, res) {
        try {
            const userId = req.user._id;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            })
            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            // Calculate items by seller for shipping calculation
            const itemsBySeller = {};
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

            res.json({
                success: true,
                message: 'Cart summary retrieved successfully',
                data: {
                    cart,
                    itemsBySeller: Object.values(itemsBySeller),
                    summary: cart.summary,
                    appliedCoupon: cart.appliedCoupon,
                    shippingAddress: cart.shippingAddress,
                    billingAddress: cart.billingAddress,
                    paymentMethod: cart.paymentMethod,
                    shippingMethod: cart.shippingMethod
                }
            });
        } catch (error) {
            console.error('Get cart summary error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to get cart summary'
            });
        }
    }

    // Validate cart items (check stock and availability)
    async validateCart(req, res) {
        try {
            const userId = req.user._id;

            // Get cart
            const cart = await Cart.findOne({
                user: userId,
                status: 'active'
            })
            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            if (cart.isEmpty()) {
                return res.json({
                    success: true,
                    message: 'Cart is empty',
                    data: {
                        valid: true,
                        issues: []
                    }
                });
            }

            // Validate each item
            const validationResults = [];
            const issues = [];

            for (const item of cart.items) {
                const product = item.product;
                const validation = {
                    productId: product._id,
                    name: product.name,
                    requestedQuantity: item.quantity,
                    availableStock: product.stock,
                    price: item.price,
                    currentPrice: product.price,
                    isAvailable: product.status === 'active',
                    issues: []
                };

                // Check stock availability
                if (product.stock < item.quantity) {
                    validation.issues.push(`Only ${product.stock} items available in stock`);
                    issues.push({
                        productId: product._id,
                        productName: product.name,
                        issue: 'insufficient_stock',
                        available: product.stock,
                        requested: item.quantity
                    });
                }

                // Check product status
                if (product.status !== 'active') {
                    validation.issues.push('Product is not available for purchase');
                    issues.push({
                        productId: product._id,
                        productName: product.name,
                        issue: 'product_inactive',
                        status: product.status
                    });
                }

                // Check price changes
                if (item.price !== product.price) {
                    validation.issues.push(`Price has changed from $${item.price} to $${product.price}`);
                    issues.push({
                        productId: product._id,
                        productName: product.name,
                        issue: 'price_changed',
                        oldPrice: item.price,
                        newPrice: product.price
                    });
                }

                validation.isValid = validation.issues.length === 0;
                validationResults.push(validation);
            }

            const isValid = issues.length === 0;

            res.json({
                success: true,
                message: isValid ? 'Cart is valid' : 'Cart validation issues found',
                data: {
                    valid: isValid,
                    validationResults,
                    issues,
                    cart: isValid ? cart : null
                }
            });
        } catch (error) {
            console.error('Validate cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to validate cart'
            });
        }
    }

    // Get abandoned carts (admin only)
    async getAbandonedCarts(req, res) {
        try {
            const { days = 1, page = 1, limit = 20 } = req.query;

            // Only admin can access this endpoint
            if (req.user.role !== 'admin' && req.user.role !== 'super_admin') {
                return res.status(403).json({
                    success: false,
                    message: 'Access denied. Admin privileges required.'
                });
            }

            const cutoffDate = new Date(Date.now() - days * 24 * 60 * 60 * 1000);

            const [carts, total] = await Promise.all([
                Cart.find({
                    status: 'abandoned',
                    lastActivity: { $lt: cutoffDate }
                })
                    .sort({ lastActivity: -1 })
                    .skip((page - 1) * limit)
                    .limit(parseInt(limit))
                    .lean(),
                Cart.countDocuments({
                    status: 'abandoned',
                    lastActivity: { $lt: cutoffDate }
                })
            ]);

            const totalPages = Math.ceil(total / limit);

            res.json({
                success: true,
                message: 'Abandoned carts retrieved successfully',
                data: {
                    carts,
                    pagination: {
                        page: parseInt(page),
                        limit: parseInt(limit),
                        total,
                        totalPages,
                        hasNextPage: page < totalPages,
                        hasPrevPage: page > 1
                    },
                    filters: {
                        days,
                        cutoffDate
                    }
                }
            });
        } catch (error) {
            console.error('Get abandoned carts error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to retrieve abandoned carts'
            });
        }
    }

    // Clean up expired carts (admin only)
    async cleanupExpiredCarts(req, res) {
        try {
            // Only admin can access this endpoint
            if (req.user.role !== 'admin' && req.user.role !== 'super_admin') {
                return res.status(403).json({
                    success: false,
                    message: 'Access denied. Admin privileges required.'
                });
            }

            const result = await Cart.cleanupExpiredCarts();

            res.json({
                success: true,
                message: 'Expired carts cleaned up successfully',
                data: {
                    deletedCount: result.deletedCount,
                    timestamp: new Date()
                }
            });
        } catch (error) {
            console.error('Cleanup expired carts error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to cleanup expired carts'
            });
        }
    }

    // Get cart by ID (admin only)
    async getCartById(req, res) {
        try {
            const { cartId } = req.params;

            // Only admin can access this endpoint
            if (req.user.role !== 'admin' && req.user.role !== 'super_admin') {
                return res.status(403).json({
                    success: false,
                    message: 'Access denied. Admin privileges required.'
                });
            }

            const cart = await Cart.findById(cartId)
                .populate('user', 'name email phoneNumber')
                .populate('items.product', 'name images price stock rating')
                .populate('items.seller', 'name email companyName');

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Cart not found'
                });
            }

            res.json({
                success: true,
                message: 'Cart retrieved successfully',
                data: cart
            });
        } catch (error) {
            console.error('Get cart by ID error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to get cart'
            });
        }
    }

    // Restore abandoned cart
    async restoreCart(req, res) {
        try {
            const userId = req.user._id;
            const { cartId } = req.params;

            // Get cart
            const cart = await Cart.findOne({
                _id: cartId,
                user: userId,
                status: 'abandoned'
            });

            if (!cart) {
                return res.status(404).json({
                    success: false,
                    message: 'Abandoned cart not found'
                });
            }

            // Restore cart
            await cart.restore();

            // Reload cart with populated data
            const updatedCart = await Cart.findById(cart._id)
            res.json({
                success: true,
                message: 'Cart restored successfully',
                data: updatedCart
            });
        } catch (error) {
            console.error('Restore cart error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to restore cart'
            });
        }
    }
}

module.exports = new CartController();