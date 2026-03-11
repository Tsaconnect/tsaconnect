// models/Cart.js - Cart model
const mongoose = require('mongoose');

const cartItemSchema = new mongoose.Schema({
    product: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'Product',
        required: [true, 'Product is required']
    },
    seller: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'User',
        required: [true, 'Seller is required']
    },
    quantity: {
        type: Number,
        required: [true, 'Quantity is required'],
        min: [1, 'Quantity must be at least 1'],
        default: 1
    },
    price: {
        type: Number,
        required: [true, 'Price is required'],
        min: [0, 'Price cannot be negative']
    },
    selectedAttributes: [{
        name: String,
        value: String
    }],
    notes: String,
    addedAt: {
        type: Date,
        default: Date.now
    },
    updatedAt: {
        type: Date,
        default: Date.now
    }
}, {
    _id: true
});

const cartSchema = new mongoose.Schema({
    user: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'User',
        required: [true, 'User is required'],
        unique: true
    },

    // Cart items grouped by seller for better shipping calculation
    items: [cartItemSchema],

    // Cart summary
    summary: {
        totalItems: {
            type: Number,
            default: 0
        },
        totalQuantity: {
            type: Number,
            default: 0
        },
        subtotal: {
            type: Number,
            default: 0
        },
        shipping: {
            type: Number,
            default: 0
        },
        tax: {
            type: Number,
            default: 0
        },
        discount: {
            type: Number,
            default: 0
        },
        total: {
            type: Number,
            default: 0
        }
    },

    // Shipping information
    shippingAddress: {
        name: String,
        phoneNumber: String,
        address: String,
        city: String,
        state: String,
        country: String,
        postalCode: String,
        isDefault: {
            type: Boolean,
            default: false
        }
    },

    // Billing information
    billingAddress: {
        sameAsShipping: {
            type: Boolean,
            default: true
        },
        name: String,
        phoneNumber: String,
        address: String,
        city: String,
        state: String,
        country: String,
        postalCode: String
    },

    // Payment information
    paymentMethod: {
        type: String,
        enum: ['card', 'bank_transfer', 'wallet', 'cash_on_delivery', 'crypto'],
        default: 'card'
    },
    paymentDetails: {
        cardLastFour: String,
        cardBrand: String,
        bankName: String,
        accountNumber: String,
        walletAddress: String
    },

    // Discounts and coupons
    appliedCoupon: {
        code: String,
        discountType: {
            type: String,
            enum: ['percentage', 'fixed', 'free_shipping'],
            default: 'percentage'
        },
        discountValue: Number,
        maxDiscount: Number,
        minPurchase: Number,
        expiresAt: Date,
        appliedAt: Date
    },

    // Shipping options
    shippingMethod: {
        type: String,
        enum: ['standard', 'express', 'next_day', 'pickup'],
        default: 'standard'
    },
    shippingProvider: String,
    estimatedDelivery: {
        from: Date,
        to: Date
    },

    // Cart metadata
    sessionId: String, // For guest users
    currency: {
        type: String,
        default: 'USD',
        enum: ['USD', 'EUR', 'GBP', 'NGN', 'GHS', 'KES', 'ZAR']
    },
    language: {
        type: String,
        default: 'en',
        enum: ['en', 'fr', 'es', 'pt', 'sw']
    },

    // Cart status
    status: {
        type: String,
        enum: ['active', 'abandoned', 'converted', 'expired'],
        default: 'active'
    },

    // Timestamps for cart lifecycle
    lastActivity: {
        type: Date,
        default: Date.now
    },
    abandonedAt: Date,
    convertedAt: Date,
    expiresAt: {
        type: Date,
        default: () => new Date(Date.now() + 30 * 24 * 60 * 60 * 1000) // 30 days
    }
}, {
    timestamps: true,
    toJSON: { virtuals: true },
    toObject: { virtuals: true }
});

// Virtual for items grouped by seller
cartSchema.virtual('itemsBySeller').get(function () {
    const grouped = {};

    this.items.forEach(item => {
        const sellerId = item.seller.toString();
        if (!grouped[sellerId]) {
            grouped[sellerId] = {
                seller: item.seller,
                items: [],
                subtotal: 0,
                shipping: 0
            };
        }
        grouped[sellerId].items.push(item);
        grouped[sellerId].subtotal += item.price * item.quantity;
    });

    return Object.values(grouped);
});

// Virtual for available coupons based on cart value
cartSchema.virtual('availableCoupons').get(function () {
    // This would be populated from active coupons in the system
    return [];
});

// Calculate cart summary
cartSchema.methods.calculateSummary = function () {
    const summary = {
        totalItems: this.items.length,
        totalQuantity: 0,
        subtotal: 0,
        shipping: this.summary.shipping || 0,
        tax: this.summary.tax || 0,
        discount: this.summary.discount || 0,
        total: 0
    };

    // Calculate totals from items
    this.items.forEach(item => {
        summary.totalQuantity += item.quantity;
        summary.subtotal += item.price * item.quantity;
    });

    // Calculate tax (example: 10% of subtotal)
    summary.tax = summary.subtotal * 0.1;

    // Apply discount if coupon is applied
    if (this.appliedCoupon && this.appliedCoupon.code) {
        const coupon = this.appliedCoupon;

        if (coupon.discountType === 'percentage') {
            const discount = summary.subtotal * (coupon.discountValue / 100);
            summary.discount = coupon.maxDiscount ? Math.min(discount, coupon.maxDiscount) : discount;
        } else if (coupon.discountType === 'fixed') {
            summary.discount = Math.min(coupon.discountValue, summary.subtotal);
        } else if (coupon.discountType === 'free_shipping') {
            summary.discount = summary.shipping;
            summary.shipping = 0;
        }
    }

    // Calculate total
    summary.total = summary.subtotal + summary.shipping + summary.tax - summary.discount;

    // Ensure total is not negative
    summary.total = Math.max(0, summary.total);

    return summary;
};

// Add item to cart
cartSchema.methods.addItem = async function (itemData) {
    const { product, seller, quantity = 1, price, selectedAttributes, notes } = itemData;

    // Check if item already exists in cart
    const existingItemIndex = this.items.findIndex(
        item => item.product.toString() === product.toString() &&
            JSON.stringify(item.selectedAttributes) === JSON.stringify(selectedAttributes)
    );

    if (existingItemIndex > -1) {
        // Update existing item quantity
        this.items[existingItemIndex].quantity += quantity;
        this.items[existingItemIndex].updatedAt = new Date();
    } else {
        // Add new item
        this.items.push({
            product,
            seller,
            quantity,
            price,
            selectedAttributes: selectedAttributes || [],
            notes,
            addedAt: new Date(),
            updatedAt: new Date()
        });
    }

    // Update summary
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Update item quantity
cartSchema.methods.updateItemQuantity = async function (itemId, quantity) {
    const itemIndex = this.items.findIndex(item => item._id.toString() === itemId);

    if (itemIndex === -1) {
        throw new Error('Item not found in cart');
    }

    if (quantity < 1) {
        // Remove item if quantity is 0 or negative
        this.items.splice(itemIndex, 1);
    } else {
        this.items[itemIndex].quantity = quantity;
        this.items[itemIndex].updatedAt = new Date();
    }

    // Update summary
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Remove item from cart
cartSchema.methods.removeItem = async function (itemId) {
    const initialLength = this.items.length;
    this.items = this.items.filter(item => item._id.toString() !== itemId);

    if (this.items.length === initialLength) {
        throw new Error('Item not found in cart');
    }

    // Update summary
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Clear cart
cartSchema.methods.clearCart = async function () {
    this.items = [];
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Apply coupon
cartSchema.methods.applyCoupon = async function (couponCode, couponData) {
    // Check if coupon is valid (you would typically fetch this from a Coupon model)
    const { discountType, discountValue, maxDiscount, minPurchase, expiresAt } = couponData;

    if (expiresAt && new Date(expiresAt) < new Date()) {
        throw new Error('Coupon has expired');
    }

    if (minPurchase && this.summary.subtotal < minPurchase) {
        throw new Error(`Minimum purchase of $${minPurchase} required`);
    }

    this.appliedCoupon = {
        code: couponCode,
        discountType,
        discountValue,
        maxDiscount,
        minPurchase,
        expiresAt,
        appliedAt: new Date()
    };

    // Update summary with discount
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Remove coupon
cartSchema.methods.removeCoupon = async function () {
    this.appliedCoupon = undefined;
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Update shipping address
cartSchema.methods.updateShippingAddress = async function (addressData) {
    this.shippingAddress = {
        ...this.shippingAddress,
        ...addressData
    };

    this.lastActivity = new Date();

    return this.save();
};

// Update billing address
cartSchema.methods.updateBillingAddress = async function (addressData) {
    if (addressData.sameAsShipping !== undefined) {
        this.billingAddress.sameAsShipping = addressData.sameAsShipping;

        if (addressData.sameAsShipping) {
            // Copy shipping address to billing address
            this.billingAddress = {
                sameAsShipping: true,
                name: this.shippingAddress.name,
                phoneNumber: this.shippingAddress.phoneNumber,
                address: this.shippingAddress.address,
                city: this.shippingAddress.city,
                state: this.shippingAddress.state,
                country: this.shippingAddress.country,
                postalCode: this.shippingAddress.postalCode
            };
        }
    } else {
        this.billingAddress = {
            ...this.billingAddress,
            ...addressData,
            sameAsShipping: false
        };
    }

    this.lastActivity = new Date();

    return this.save();
};

// Update payment method
cartSchema.methods.updatePaymentMethod = async function (paymentData) {
    this.paymentMethod = paymentData.method;

    if (paymentData.details) {
        this.paymentDetails = {
            ...this.paymentDetails,
            ...paymentData.details
        };
    }

    this.lastActivity = new Date();

    return this.save();
};

// Update shipping method
cartSchema.methods.updateShippingMethod = async function (shippingData) {
    const { method, provider, estimatedDelivery } = shippingData;

    this.shippingMethod = method;
    this.shippingProvider = provider;

    if (estimatedDelivery) {
        this.estimatedDelivery = estimatedDelivery;
    }

    // Recalculate shipping cost based on method
    // This would typically call a shipping calculation service
    const shippingCosts = {
        standard: 5.00,
        express: 12.00,
        next_day: 25.00,
        pickup: 0.00
    };

    this.summary.shipping = shippingCosts[method] || 0;
    this.summary = this.calculateSummary();
    this.lastActivity = new Date();

    return this.save();
};

// Convert cart to order
cartSchema.methods.convertToOrder = async function () {
    // This method would typically create an Order document
    // and update the cart status

    this.status = 'converted';
    this.convertedAt = new Date();
    this.lastActivity = new Date();

    // Clear the cart after conversion
    this.items = [];
    this.summary = this.calculateSummary();
    this.appliedCoupon = undefined;

    return this.save();
};

// Check if cart is empty
cartSchema.methods.isEmpty = function () {
    return this.items.length === 0;
};

// Check cart expiration
cartSchema.methods.isExpired = function () {
    return new Date() > this.expiresAt;
};

// Mark cart as abandoned
cartSchema.methods.markAsAbandoned = async function () {
    if (this.status === 'active' && !this.isEmpty() && this.lastActivity) {
        const hoursSinceLastActivity = (new Date() - this.lastActivity) / (1000 * 60 * 60);

        if (hoursSinceLastActivity > 24) { // 24 hours of inactivity
            this.status = 'abandoned';
            this.abandonedAt = new Date();
            return this.save();
        }
    }

    return this;
};

// Restore abandoned cart
cartSchema.methods.restore = async function () {
    if (this.status === 'abandoned') {
        this.status = 'active';
        this.abandonedAt = undefined;
        this.lastActivity = new Date();
        return this.save();
    }

    return this;
};

// Middleware to update summary before save
cartSchema.pre('save', function () {
    // Recalculate summary before saving
    this.summary = this.calculateSummary();

    // Update last activity
    this.lastActivity = new Date();

    // Update expiresAt if cart is active and not empty
    if (this.status === 'active' && !this.isEmpty()) {
        this.expiresAt = new Date(Date.now() + 30 * 24 * 60 * 60 * 1000); // Reset to 30 days
    }
});

// Indexes for better query performance
cartSchema.index({ user: 1 });
cartSchema.index({ status: 1 });
cartSchema.index({ lastActivity: 1 });
cartSchema.index({ expiresAt: 1 });
cartSchema.index({ 'items.product': 1 });
cartSchema.index({ 'items.seller': 1 });

// Static method to find cart by user ID
cartSchema.statics.findByUserId = function (userId) {
    return this.findOne({ user: userId }).populate('items.product').populate('items.seller', 'name email companyName');
};

// Static method to find abandoned carts
cartSchema.statics.findAbandonedCarts = function (days = 1) {
    const cutoffDate = new Date(Date.now() - days * 24 * 60 * 60 * 1000);

    return this.find({
        status: 'abandoned',
        lastActivity: { $lt: cutoffDate }
    }).populate('user', 'name email');
};

// Static method to clean up expired carts
cartSchema.statics.cleanupExpiredCarts = async function () {
    const result = await this.deleteMany({
        expiresAt: { $lt: new Date() },
        status: { $ne: 'converted' }
    });

    return result;
};

module.exports = mongoose.model('Cart', cartSchema);