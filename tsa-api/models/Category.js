const mongoose = require('mongoose');

const categorySchema = new mongoose.Schema({
    title: {
        type: String,
        required: true,
        trim: true,
        unique: true
    },
    description: {
        type: String,
        trim: true
    },
    type: {
        type: String,
        enum: ['Product', 'Service', 'Both'],
        default: 'Product'
    },
    parentCategory: {
        type: mongoose.Schema.Types.ObjectId,
        ref: 'Category',
        default: null
    },
    icon: {
        type: String
    },
    color: {
        type: String,
        default: '#666666'
    },
    isActive: {
        type: Boolean,
        default: true
    },
    order: {
        type: Number,
        default: 0
    },
    productCount: {
        type: Number,
        default: 0
    }
}, {
    timestamps: true
});

// Indexes
categorySchema.index({ type: 1, isActive: 1 });
categorySchema.index({ title: 'text' });

// Virtual for child categories
categorySchema.virtual('children', {
    ref: 'Category',
    localField: '_id',
    foreignField: 'parentCategory'
});

// Update product count
categorySchema.statics.updateProductCount = async function (categoryId) {
    const Product = mongoose.model('Product');
    const count = await Product.countDocuments({
        category: categoryId,
        status: 'active'
    });

    await this.findByIdAndUpdate(categoryId, { productCount: count });
};

module.exports = mongoose.model('Category', categorySchema);