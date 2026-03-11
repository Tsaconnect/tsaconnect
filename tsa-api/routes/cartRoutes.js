// routes/cartRoutes.js
const express = require('express');
const router = express.Router();
const CartController = require('../controllers/cartController');
const auth = require('../middleware/auth');
const adminAuth = require('../middleware/adminAuth');
// Apply authentication middleware to all routes
router.use(auth);

// Cart operations
router.get('/', CartController.getOrCreateCart);
router.post('/items', CartController.addToCart);
router.put('/items/:itemId', CartController.updateCartItem);
router.delete('/items/:itemId', CartController.removeFromCart);
router.delete('/clear', CartController.clearCart);

// Coupon operations
router.post('/coupon', CartController.applyCoupon);
router.delete('/coupon', CartController.removeCoupon);

// Address operations
router.put('/shipping-address', CartController.updateShippingAddress);
router.put('/billing-address', CartController.updateBillingAddress);

// Shipping and payment
router.put('/shipping-method', CartController.updateShippingMethod);
router.put('/payment-method', CartController.updatePaymentMethod);

// Checkout and validation
router.post('/checkout', CartController.convertToOrder);
router.get('/validate', CartController.validateCart);
router.get('/summary', CartController.getCartSummary);

// Cart restoration
router.post('/restore/:cartId', CartController.restoreCart);

// Admin operations (require admin role in controller)
router.use(adminAuth);
router.get('/admin/abandoned', CartController.getAbandonedCarts);
router.get('/admin/:cartId', CartController.getCartById);
router.delete('/admin/cleanup', CartController.cleanupExpiredCarts);

module.exports = router;