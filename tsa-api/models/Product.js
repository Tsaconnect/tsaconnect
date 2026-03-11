const mongoose = require('mongoose');

const productSchema = new mongoose.Schema({
    userId: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'User',
        required: true
    },
    name: {
        type: String,
        required: true,
        trim: true
    },
    description: {
        type: String,
        required: true,
        trim: true
    },
    price: {
        type: Number,
        required: true,
        min: 0
    },
    stock: {
        type: Number,
        required: true,
        default: 0,
        min: 0
    },
    category: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'Category'
    },
    categoryName: {
        type: String
    },
    location: {
        type: String,
        required: true
    },
    phoneNumber: {
        type: String,
        required: true
    },
    email: {
        type: String,
        required: true,
        lowercase: true,
        trim: true
    },
    companyName: {
        type: String,
        trim: true
    },
    images: [{
        url: {
            type: String,
            required: true
        },
        publicId: {
            type: String
        },
        order: {
            type: Number,
            default: 0
        }
    }],
    attributes: [{
        name: {
            type: String,
            required: true,
            trim: true
        },
        value: {
            type: String,
            required: true,
            trim: true
        }
    }],
    status: {
        type: String,
        enum: ['active', 'inactive', 'sold_out', 'pending_review'],
        default: 'active'
    },
    type: {
        type: String,
        enum: ['Product', 'Service'],
        default: 'Product'
    },
    isFeatured: {
        type: Boolean,
        default: false
    },
    views: {
        type: Number,
        default: 0
    },
    sales: {
        type: Number,
        default: 0
    },
    rating: {
        average: {
            type: Number,
            default: 0,
            min: 0,
            max: 5
        },
        count: {
            type: Number,
            default: 0
        }
    },
    metadata: {
        sku: String,
        weight: Number,
        dimensions: {
            length: Number,
            width: Number,
            height: Number
        },
        tags: [String]
    }
}, {
    timestamps: true
});

// Indexes for faster queries
productSchema.index({ userId: 1, createdAt: -1 });
productSchema.index({ category: 1, status: 1 });
productSchema.index({ name: 'text', description: 'text' });
productSchema.index({ status: 1, isFeatured: 1 });

// Virtual for formatted price
productSchema.virtual('formattedPrice').get(function () {
    return `$${this.price.toFixed(2)}`;
});

// Virtual for in stock status
productSchema.virtual('inStock').get(function () {
    return this.stock > 0;
});

// Method to update stock
productSchema.methods.updateStock = function (quantity) {
    if (quantity > this.stock) {
        throw new Error('Insufficient stock');
    }
    this.stock -= quantity;
    this.sales += quantity;
    return this.save();
};

// Method to add review
productSchema.methods.addReview = function (rating) {
    const totalRating = this.rating.average * this.rating.count + rating;
    this.rating.count += 1;
    this.rating.average = totalRating / this.rating.count;
    return this.save();
};

module.exports = mongoose.model('Product', productSchema);