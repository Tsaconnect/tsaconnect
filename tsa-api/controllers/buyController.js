const httpStatus = require('http-status');
const { cartService } = require('../services');

class CartController {
    // Add item to cart
    async addToCart(req, res) {
        try {
            const { productId, quantity } = req.body;
            const userId = req.user._id;

            const cart = await cartService.addToCart(userId, productId, quantity);

            res.status(httpStatus.OK).json({
                success: true,
                data: cart
            });
        } catch (error) {
            console.error('Add to cart error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to add item to cart'
            });
        }
    }

    // Get user cart
    async getCart(req, res) {
        try {
            const userId = req.user._id;
            const cart = await cartService.getCartItems(userId);

            res.status(httpStatus.OK).json({
                success: true,
                data: cart
            });
        } catch (error) {
            console.error('Get cart error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to fetch cart'
            });
        }
    }

    // Remove item from cart
    async removeItemFromCart(req, res) {
        try {
            const userId = req.user._id;
            const { productId } = req.params;

            await cartService.removeItemFromCart(userId, productId);

            res.status(httpStatus.OK).json({
                success: true,
                message: 'Item removed from cart successfully'
            });
        } catch (error) {
            console.error('Remove item error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to remove item from cart'
            });
        }
    }

    // Clear cart
    async clearCart(req, res) {
        try {
            const userId = req.user._id;

            await cartService.clearCart(userId);

            res.status(httpStatus.OK).json({
                success: true,
                message: 'Cart cleared successfully'
            });
        } catch (error) {
            console.error('Clear cart error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to clear cart'
            });
        }
    }

    // Increase item quantity
    async increaseQuantity(req, res) {
        try {
            const { productId } = req.params;
            const { quantity = 1 } = req.body;
            const userId = req.user._id;

            const cart = await cartService.increaseQuantity(userId, productId, quantity);

            res.status(httpStatus.OK).json({
                success: true,
                data: cart
            });
        } catch (error) {
            console.error('Increase quantity error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to increase quantity'
            });
        }
    }

    // Decrease item quantity
    async decreaseQuantity(req, res) {
        try {
            const { productId } = req.params;
            const { quantity = 1 } = req.body;
            const userId = req.user._id;

            const cart = await cartService.decreaseQuantity(userId, productId, quantity);

            res.status(httpStatus.OK).json({
                success: true,
                data: cart
            });
        } catch (error) {
            console.error('Decrease quantity error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to decrease quantity'
            });
        }
    }

    // Get cart summary/total
    async getCartSummary(req, res) {
        try {
            const userId = req.user._id;
            const cart = await cartService.getCartItems(userId);

            // Calculate summary
            const summary = {
                totalItems: cart.items.reduce((sum, item) => sum + item.quantity, 0),
                subtotal: cart.items.reduce((sum, item) => sum + (item.price * item.quantity), 0),
                shipping: 0, // You can implement shipping logic
                tax: 0, // You can implement tax logic
                total: cart.items.reduce((sum, item) => sum + (item.price * item.quantity), 0)
            };

            res.status(httpStatus.OK).json({
                success: true,
                data: {
                    cart,
                    summary
                }
            });
        } catch (error) {
            console.error('Get cart summary error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to get cart summary'
            });
        }
    }

    // Merge guest cart with user cart
    async mergeCart(req, res) {
        try {
            const userId = req.user._id;
            const { guestCartId } = req.body;

            // Implementation depends on your cart service
            const cart = await cartService.mergeCarts(guestCartId, userId);

            res.status(httpStatus.OK).json({
                success: true,
                message: 'Cart merged successfully',
                data: cart
            });
        } catch (error) {
            console.error('Merge cart error:', error);
            res.status(error.statusCode || httpStatus.INTERNAL_SERVER_ERROR).json({
                success: false,
                message: error.message || 'Failed to merge cart'
            });
        }
    }
}

module.exports = new CartController();