const httpStatus = require('http-status');
const ApiError = require('../utils/ApiError');
const { Cart } = require('../models');
const { getAdById } = require('./advert.service');

class CartService {
    constructor() {
        this.CartModel = null;
        this.initializeModel();
    }

    async initializeModel() {
        this.CartModel = await Cart();
    }

    // Helper method for parameter validation
    validateCartParams(userId, productId, quantity) {
        if (!userId || !productId || quantity == null) {
            throw new ApiError(httpStatus.BAD_REQUEST, 'userId, productId, and quantity are required');
        }
        if (quantity <= 0) {
            throw new ApiError(httpStatus.BAD_REQUEST, 'Quantity must be a positive number');
        }
    }

    /**
     * Add an item to the cart
     * @param {ObjectId} userId - The ID of the user whose cart is being modified
     * @param {ObjectId} productId - The ID of the product being added
     * @param {number} quantity - quantity of the item being added to the cart
     * @returns {Promise<Cart>} - The updated cart
     */
    async addToCart(userId, productId, quantity) {
        try {
            await this.initializeModel();
            this.validateCartParams(userId, productId, quantity);

            // Check if the product already exists in the user's cart
            const existingCartItem = await this.CartModel.findOne({ userId, productId });
            if (existingCartItem) {
                // If the product already exists, update the quantity
                existingCartItem.quantity += quantity;
                await existingCartItem.save();
                return existingCartItem;
            }

            const product = await getAdById(productId);
            if (!product) {
                throw new ApiError(httpStatus.NOT_FOUND, 'Product not found');
            }

            // If the product doesn't exist, create a new cart item
            const newCartItem = new this.CartModel({
                userId,
                productId,
                quantity,
                name: product.name,
                price: product.price,
                image: product.images?.[0] || null,
            });
            await newCartItem.save();
            return newCartItem;
        } catch (error) {
            console.error('Add to cart service error:', error);
            throw error;
        }
    }

    /**
     * Get cart items for a user
     * @param {ObjectId} userId
     * @returns {Promise<Cart[]>}
     */
    async getCartItems(userId) {
        try {
            await this.initializeModel();

            if (!userId) {
                throw new ApiError(httpStatus.BAD_REQUEST, 'userId is required');
            }

            const cartItems = await this.CartModel.find({ userId });
            return cartItems;
        } catch (error) {
            console.error('Get cart items service error:', error);
            throw error;
        }
    }

    /**
     * Remove item from cart by productId
     * @param {ObjectId} userId - User ID
     * @param {ObjectId} productId - Product ID
     * @returns {Promise<void>}
     */
    async removeItemFromCart(userId, productId) {
        try {
            await this.initializeModel();

            if (!userId || !productId) {
                throw new ApiError(httpStatus.BAD_REQUEST, 'userId and productId are required');
            }

            const result = await this.CartModel.deleteOne({ userId, productId });

            if (result.deletedCount === 0) {
                throw new ApiError(httpStatus.NOT_FOUND, 'Cart item not found');
            }
        } catch (error) {
            console.error('Remove item from cart service error:', error);
            throw error;
        }
    }

    /**
     * Clear cart
     * @param {ObjectId} userId - User ID
     * @returns {Promise<void>}
     */
    async clearCart(userId) {
        try {
            await this.initializeModel();

            if (!userId) {
                throw new ApiError(httpStatus.BAD_REQUEST, 'userId is required');
            }

            await this.CartModel.deleteMany({ userId });
        } catch (error) {
            console.error('Clear cart service error:', error);
            throw error;
        }
    }

    /**
     * Increase the quantity of an item in the cart
     * @param {ObjectId} userId - User ID
     * @param {ObjectId} productId - Product ID
     * @param {number} quantity - quantity to increase by
     * @returns {Promise<Cart>} - Updated cart
     */
    async increaseQuantity(userId, productId, quantity = 1) {
        try {
            await this.initializeModel();
            this.validateCartParams(userId, productId, quantity);

            const cartItem = await this.CartModel.findOne({ userId, productId });
            if (!cartItem) {
                throw new ApiError(httpStatus.NOT_FOUND, 'Cart item not found');
            }

            cartItem.quantity += quantity;
            await cartItem.save();

            return cartItem;
        } catch (error) {
            console.error('Increase quantity service error:', error);
            throw error;
        }
    }

    /**
     * Decrease the quantity of an item in the cart
     * @param {ObjectId} userId - User ID
     * @param {ObjectId} productId - Product ID
     * @param {number} quantity - quantity to decrease by
     * @returns {Promise<Cart>} - Updated cart
     */
    async decreaseQuantity(userId, productId, quantity = 1) {
        try {
            await this.initializeModel();
            this.validateCartParams(userId, productId, quantity);

            const cartItem = await this.CartModel.findOne({ userId, productId });
            if (!cartItem) {
                throw new ApiError(httpStatus.NOT_FOUND, 'Cart item not found');
            }

            cartItem.quantity -= quantity;

            if (cartItem.quantity <= 0) {
                await this.CartModel.deleteOne({ userId, productId });
                return { deleted: true, message: 'Item removed from cart' };
            }

            await cartItem.save();
            return cartItem;
        } catch (error) {
            console.error('Decrease quantity service error:', error);
            throw error;
        }
    }

    /**
     * Get cart summary/total
     * @param {ObjectId} userId
     * @returns {Promise<Object>}
     */
    async getCartSummary(userId) {
        try {
            await this.initializeModel();

            const cartItems = await this.getCartItems(userId);

            const summary = {
                totalItems: cartItems.reduce((sum, item) => sum + item.quantity, 0),
                subtotal: cartItems.reduce((sum, item) => sum + (item.price * item.quantity), 0),
                totalUniqueItems: cartItems.length,
                items: cartItems
            };

            return summary;
        } catch (error) {
            console.error('Get cart summary service error:', error);
            throw error;
        }
    }

    /**
     * Check if item exists in cart
     * @param {ObjectId} userId
     * @param {ObjectId} productId
     * @returns {Promise<boolean>}
     */
    async isItemInCart(userId, productId) {
        try {
            await this.initializeModel();

            const cartItem = await this.CartModel.findOne({ userId, productId });
            return !!cartItem;
        } catch (error) {
            console.error('Check item in cart service error:', error);
            throw error;
        }
    }

    /**
     * Get cart item by user and product
     * @param {ObjectId} userId
     * @param {ObjectId} productId
     * @returns {Promise<Cart>}
     */
    async getCartItem(userId, productId) {
        try {
            await this.initializeModel();

            const cartItem = await this.CartModel.findOne({ userId, productId });
            return cartItem;
        } catch (error) {
            console.error('Get cart item service error:', error);
            throw error;
        }
    }

    /**
     * Get cart count for a user
     * @param {ObjectId} userId
     * @returns {Promise<number>}
     */
    async getCartCount(userId) {
        try {
            await this.initializeModel();

            const count = await this.CartModel.countDocuments({ userId });
            return count;
        } catch (error) {
            console.error('Get cart count service error:', error);
            throw error;
        }
    }
}

module.exports = new CartService();