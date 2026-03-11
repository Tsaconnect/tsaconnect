const Product = require('../models/Product');
const Category = require('../models/Category');
const uploadToCloudinary = require('../middleware/cloudinaryUpload');
const cloudinary = require('cloudinary').v2;
const mongoose = require('mongoose');
class ProductController {
    // Create new product
    async createProduct(req, res) {
        console.log('=== CREATE PRODUCT DEBUG ===');
        console.log('Request body:', req.body);
        console.log('Request files exist:', !!(req.files && req.files.length));
        console.log('Request files count:', req.files ? req.files.length : 0);
        console.log('=== END DEBUG ===');

        try {
            const userId = req.user._id;
            const {
                name,
                description,
                price,
                stock,
                category,
                location,
                phoneNumber,
                email,
                companyName,
                attributes,
                type = 'Product'
            } = req.body;

            // Validate required fields
            if (!name || !description || !price || !location || !phoneNumber || !email) {
                return res.status(400).json({
                    success: false,
                    message: 'Missing required fields'
                });
            }

            // Validate category if provided
            let categoryData = null;
            if (category) {
                categoryData = await Category.findById(category);
                if (!categoryData) {
                    return res.status(404).json({
                        success: false,
                        message: 'Category not found'
                    });
                }
            }

            // Handle image uploads if provided
            const uploadedImages = [];
            if (req.files && req.files.length > 0) {
                for (let i = 0; i < req.files.length; i++) {
                    const file = req.files[i];

                    try {
                        // Upload to Cloudinary using buffer like in createCategory
                        let uploadResult;
                        if (file.buffer) {
                            // Using buffer upload (like in createCategory)
                            uploadResult = await uploadToCloudinary(file.buffer, 'products');
                        } else {
                            // Fallback to path upload
                            uploadResult = await cloudinary.uploader.upload(file.path, {
                                folder: 'products',
                                transformation: [
                                    { width: 800, height: 800, crop: 'fill', quality: 'auto' }
                                ]
                            });
                        }

                        uploadedImages.push({
                            url: uploadResult.secure_url,
                            publicId: uploadResult.public_id,
                            format: uploadResult.format,
                            width: uploadResult.width,
                            height: uploadResult.height,
                            order: i
                        });

                        console.log(`Image ${i + 1} uploaded to Cloudinary:`, uploadResult.secure_url);
                    } catch (uploadError) {
                        console.error('Cloudinary upload error for image:', uploadError);
                        // Don't fail the entire request if image upload fails
                        // Continue with other images
                    }
                }
            }

            // Parse attributes if provided as JSON string
            let parsedAttributes = [];
            if (attributes) {
                try {
                    if (typeof attributes === 'string') {
                        parsedAttributes = JSON.parse(attributes);
                    } else if (Array.isArray(attributes)) {
                        parsedAttributes = attributes;
                    }
                } catch (error) {
                    console.error('Error parsing attributes:', error);
                    // Continue with empty attributes instead of failing
                }
            }

            // Create product
            const product = await Product.create({
                userId,
                name,
                description,
                price: parseFloat(price),
                stock: parseInt(stock) || 0,
                category: categoryData ? categoryData._id : null,
                categoryName: categoryData ? categoryData.title : null,
                location,
                phoneNumber,
                email,
                companyName: companyName || '',
                images: uploadedImages,
                attributes: parsedAttributes,
                type,
                status: 'active'
            });

            // Update category product count
            if (categoryData) {
                await Category.updateProductCount(categoryData._id);
            }

            res.status(201).json({
                success: true,
                message: 'Product created successfully',
                data: product
            });
        } catch (error) {
            console.error('Create product error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to create product'
            });
        }
    }

    // Get all products for user
    async getUserProducts(req, res) {
        console.log('Get user products');
        try {
            const userId = req.user._id;
            const {
                page = 1,
                limit = 20,
                status,
                category,
                search,
                sortBy = 'createdAt',
                sortOrder = 'desc'
            } = req.query;

            // Build query
            const query = { userId };

            if (status) query.status = status;
            if (category) query.category = category;

            if (search) {
                query.$or = [
                    { name: { $regex: search, $options: 'i' } },
                    { description: { $regex: search, $options: 'i' } },
                    { companyName: { $regex: search, $options: 'i' } }
                ];
            }

            // Sort
            const sort = {};
            sort[sortBy] = sortOrder === 'desc' ? -1 : 1;

            // Pagination
            const skip = (page - 1) * limit;

            const [products, total] = await Promise.all([
                Product.find(query)
                    .populate('category', 'title icon color')
                    .sort(sort)
                    .skip(skip)
                    .limit(parseInt(limit))
                    .lean(),
                Product.countDocuments(query)
            ]);

            // Calculate pagination info
            const totalPages = Math.ceil(total / limit);

            res.json({
                success: true,
                data: {
                    products,
                    pagination: {
                        page: parseInt(page),
                        limit: parseInt(limit),
                        total,
                        totalPages,
                        hasNextPage: page < totalPages,
                        hasPrevPage: page > 1
                    }
                }
            });
        } catch (error) {
            console.error('Get user products error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to fetch products'
            });
        }
    }

    // Get product by ID
    async getProductById(req, res) {
        try {
            const { productId } = req.params;
            const userId = req.user._id;

            const product = await Product.findOne({
                _id: productId
            }).populate('category', 'title icon color');

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Increment view count
            product.views += 1;
            await product.save();

            res.json({
                success: true,
                data: product
            });
        } catch (error) {
            console.error('Get product by ID error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to fetch product'
            });
        }
    }

    // Update product
    async updateProduct(req, res) {
        try {
            const { productId } = req.params;
            const userId = req.user._id;
            const updates = req.body;

            // Find product
            const product = await Product.findOne({
                _id: productId,
                userId
            });

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Handle image uploads if new images are provided
            if (req.files && req.files.length > 0) {
                // Delete old images from Cloudinary
                for (const image of product.images) {
                    if (image.publicId) {
                        try {
                            await cloudinary.uploader.destroy(image.publicId);
                        } catch (error) {
                            console.error('Error deleting old image:', error);
                        }
                    }
                }

                // Upload new images
                const uploadedImages = [];
                for (let i = 0; i < req.files.length; i++) {
                    const file = req.files[i];

                    try {
                        const result = await cloudinary.uploader.upload(file.path, {
                            folder: 'products',
                            transformation: [
                                { width: 800, height: 800, crop: 'fill', quality: 'auto' }
                            ]
                        });

                        uploadedImages.push({
                            url: result.secure_url,
                            publicId: result.public_id,
                            order: i
                        });
                    } catch (uploadError) {
                        console.error('Error uploading image:', uploadError);
                    }
                }

                updates.images = uploadedImages;
            }

            // Parse attributes if provided
            if (updates.attributes && typeof updates.attributes === 'string') {
                try {
                    updates.attributes = JSON.parse(updates.attributes);
                } catch (error) {
                    console.error('Error parsing attributes:', error);
                    delete updates.attributes;
                }
            }

            // Update numeric fields
            if (updates.price) updates.price = parseFloat(updates.price);
            if (updates.stock) updates.stock = parseInt(updates.stock);

            // Update product
            Object.keys(updates).forEach(key => {
                if (key !== '_id' && key !== 'userId') {
                    product[key] = updates[key];
                }
            });

            await product.save();

            // Update category product count if category changed
            if (updates.category) {
                await Category.updateProductCount(updates.category);
            }

            res.json({
                success: true,
                message: 'Product updated successfully',
                data: product
            });
        } catch (error) {
            console.error('Update product error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to update product'
            });
        }
    }

    // Delete product
    async deleteProduct(req, res) {
        try {
            const { productId } = req.params;
            const userId = req.user._id;

            const product = await Product.findOne({
                _id: productId,
                userId
            });

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Delete images from Cloudinary
            for (const image of product.images) {
                if (image.publicId) {
                    try {
                        await cloudinary.uploader.destroy(image.publicId);
                    } catch (error) {
                        console.error('Error deleting image:', error);
                    }
                }
            }

            // Get category before deletion
            const categoryId = product.category;

            // Delete product
            await Product.findByIdAndDelete(productId);

            // Update category product count
            if (categoryId) {
                await Category.updateProductCount(categoryId);
            }

            res.json({
                success: true,
                message: 'Product deleted successfully'
            });
        } catch (error) {
            console.error('Delete product error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to delete product'
            });
        }
    }

    // Update product stock
    async updateStock(req, res) {
        try {
            const { productId } = req.params;
            const { quantity } = req.body;
            const userId = req.user._id;

            if (!quantity || isNaN(quantity)) {
                return res.status(400).json({
                    success: false,
                    message: 'Valid quantity is required'
                });
            }

            const product = await Product.findOne({
                _id: productId,
                userId
            });

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            product.stock = parseInt(quantity);
            await product.save();

            res.json({
                success: true,
                message: 'Stock updated successfully',
                data: {
                    stock: product.stock
                }
            });
        } catch (error) {
            console.error('Update stock error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to update stock'
            });
        }
    }

    // Update product status
    async updateStatus(req, res) {
        try {
            const { productId } = req.params;
            const { status } = req.body;
            const userId = req.user._id;

            const validStatuses = ['active', 'inactive', 'sold_out', 'pending_review'];
            if (!validStatuses.includes(status)) {
                return res.status(400).json({
                    success: false,
                    message: 'Invalid status'
                });
            }

            const product = await Product.findOneAndUpdate(
                { _id: productId, userId },
                { status },
                { new: true }
            );

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            res.json({
                success: true,
                message: 'Status updated successfully',
                data: {
                    status: product.status
                }
            });
        } catch (error) {
            console.error('Update status error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to update status'
            });
        }
    }

    // Get product statistics
    async getProductStats(req, res) {
        try {
            const userId = req.user._id;

            const stats = await Product.aggregate([
                {
                    $match: { userId: mongoose.Types.ObjectId(userId) }
                },
                {
                    $group: {
                        _id: '$status',
                        count: { $sum: 1 },
                        totalValue: { $sum: { $multiply: ['$price', '$stock'] } },
                        totalStock: { $sum: '$stock' },
                        totalSales: { $sum: '$sales' },
                        totalViews: { $sum: '$views' }
                    }
                }
            ]);

            const totalProducts = await Product.countDocuments({ userId });
            const totalValue = await Product.aggregate([
                {
                    $match: { userId: mongoose.Types.ObjectId(userId) }
                },
                {
                    $group: {
                        _id: null,
                        total: { $sum: { $multiply: ['$price', '$stock'] } }
                    }
                }
            ]);

            res.json({
                success: true,
                data: {
                    stats,
                    totals: {
                        products: totalProducts,
                        value: totalValue[0]?.total || 0
                    }
                }
            });
        } catch (error) {
            console.error('Get product stats error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to fetch product statistics'
            });
        }
    }

    // Upload product images (separate endpoint for multiple uploads)
    async uploadImages(req, res) {
        try {
            const { productId } = req.params;
            const userId = req.user._id;

            if (!req.files || req.files.length === 0) {
                return res.status(400).json({
                    success: false,
                    message: 'No images provided'
                });
            }

            const product = await Product.findOne({
                _id: productId,
                userId
            });

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Upload new images
            const uploadedImages = [];
            for (let i = 0; i < req.files.length; i++) {
                const file = req.files[i];

                try {
                    const result = await cloudinary.uploader.upload(file.path, {
                        folder: 'products',
                        transformation: [
                            { width: 800, height: 800, crop: 'fill', quality: 'auto' }
                        ]
                    });

                    uploadedImages.push({
                        url: result.secure_url,
                        publicId: result.public_id,
                        order: product.images.length + i
                    });
                } catch (uploadError) {
                    console.error('Error uploading image:', uploadError);
                }
            }

            // Add new images to product
            product.images.push(...uploadedImages);
            await product.save();

            res.json({
                success: true,
                message: 'Images uploaded successfully',
                data: {
                    images: uploadedImages
                }
            });
        } catch (error) {
            console.error('Upload images error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to upload images'
            });
        }
    }

    // Delete product image
    async deleteImage(req, res) {
        try {
            const { productId, imageId } = req.params;
            const userId = req.user._id;

            const product = await Product.findOne({
                _id: productId,
                userId
            });

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Find the image
            const imageIndex = product.images.findIndex(img => img._id.toString() === imageId);
            if (imageIndex === -1) {
                return res.status(404).json({
                    success: false,
                    message: 'Image not found'
                });
            }

            const image = product.images[imageIndex];

            // Delete from Cloudinary
            if (image.publicId) {
                try {
                    await cloudinary.uploader.destroy(image.publicId);
                } catch (error) {
                    console.error('Error deleting image from Cloudinary:', error);
                }
            }

            // Remove from product
            product.images.splice(imageIndex, 1);

            // Reorder remaining images
            product.images.forEach((img, index) => {
                img.order = index;
            });

            await product.save();

            res.json({
                success: true,
                message: 'Image deleted successfully'
            });
        } catch (error) {
            console.error('Delete image error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to delete image'
            });
        }
    }
    // Get all products that are not featured
    async getNonFeaturedProducts(req, res) {
        console.log('Get non-featured products');
        try {
            const products = await Product.find({
                isFeatured: false
            });
            console.log(products);
            res.json({
                success: true,
                data: products
            });
        } catch (error) {
            console.error('Get non-featured products error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to fetch products'
            });
        }
    }
    async toggleFeatured(req, res) {
        try {
            const { id } = req.params;
            const userId = req.user._id;
            const userRole = req.user.role;

            const product = await Product.findById(id);

            if (!product) {
                return res.status(404).json({
                    success: false,
                    message: 'Product not found'
                });
            }

            // Check if user owns the product or is admin
            if (product.userId.toString() !== userId.toString() && userRole !== 'admin') {
                return res.status(403).json({
                    success: false,
                    message: 'Not authorized to update this product'
                });
            }

            // Toggle featured status
            product.isFeatured = !product.isFeatured;

            // Add to featured history if tracking
            if (product.featuredHistory) {
                product.featuredHistory.push({
                    date: new Date(),
                    action: product.isFeatured ? 'added' : 'removed',
                    by: userId
                });
            }

            await product.save();

            res.json({
                success: true,
                message: `Product ${product.isFeatured ? 'added to' : 'removed from'} featured`,
                data: {
                    productId: product._id,
                    isFeatured: product.isFeatured,
                    featuredAt: product.isFeatured ? new Date() : null
                }
            });
        } catch (error) {
            console.error('Toggle featured error:', error);
            res.status(500).json({
                success: false,
                message: 'Server error'
            });
        }
    }
    async getProducts(req, res) {
        try {
            const products = await Product.find({});
            res.json({
                success: true,
                data: products
            });
        } catch (error) {
            console.error('Get products error:', error);
            res.status(500).json({
                success: false,
                message: 'Failed to fetch products'
            });
        }
    }
    // Add this method to your ProductController class

    // Get products by category (public endpoint - no authentication required)
    async getProductsByCategory(req, res) {
        try {
            const {
                categoryId,
                subcategoryId,
                page = 1,
                limit = 20,
                sortBy = 'createdAt',
                sortOrder = 'desc',
                minPrice,
                maxPrice,
                type,
                status = 'active',
                search
            } = req.query;

            // Build query
            const query = {};

            // Filter by category
            if (categoryId) {
                query.category = categoryId;
            }

            // Filter by product type (Product/Service)
            if (type && ['Product', 'Service'].includes(type)) {
                query.type = type;
            }

            // Filter by status
            if (status && ['active', 'inactive', 'sold_out', 'pending_review'].includes(status)) {
                query.status = status;
            } else {
                // Default to active products
                query.status = 'active';
            }

            // Price range filter
            if (minPrice || maxPrice) {
                query.price = {};
                if (minPrice) query.price.$gte = parseFloat(minPrice);
                if (maxPrice) query.price.$lte = parseFloat(maxPrice);
            }

            // Search functionality
            if (search) {
                query.$or = [
                    { name: { $regex: search, $options: 'i' } },
                    { description: { $regex: search, $options: 'i' } },
                    { companyName: { $regex: search, $options: 'i' } },
                    { 'attributes.value': { $regex: search, $options: 'i' } }
                ];
            }

            // If subcategory name is provided (for marketplace navigation)
            if (subcategoryId && !mongoose.Types.ObjectId.isValid(subcategoryId)) {
                // This might be a subcategory name from the frontend
                // You can search in category names or attributes
                const categories = await Category.find({
                    title: { $regex: subcategoryId, $options: 'i' }
                });

                if (categories.length > 0) {
                    const categoryIds = categories.map(cat => cat._id);
                    query.category = { $in: categoryIds };
                }
            }

            // Sort options
            const sort = {};
            const validSortFields = ['createdAt', 'updatedAt', 'price', 'name', 'views', 'sales', 'rating.average'];

            if (validSortFields.includes(sortBy)) {
                // Handle nested sort fields
                if (sortBy.includes('.')) {
                    const parts = sortBy.split('.');
                    sort[parts[0]] = sort[parts[1]] = sortOrder === 'desc' ? -1 : 1;
                } else {
                    sort[sortBy] = sortOrder === 'desc' ? -1 : 1;
                }
            } else {
                // Default sort
                sort.createdAt = sortOrder === 'desc' ? -1 : 1;
            }

            // Pagination
            const skip = (page - 1) * limit;

            // Execute query with pagination
            const [products, total] = await Promise.all([
                Product.find(query)
                    .populate('category', 'title icon color type parentCategory')
                    .sort(sort)
                    .skip(skip)
                    .limit(parseInt(limit))
                    .select('-__v') // Exclude version key
                    .lean(),
                Product.countDocuments(query)
            ]);

            // Calculate pagination info
            const totalPages = Math.ceil(total / limit);

            // Get category details if categoryId is provided
            let categoryDetails = null;
            if (categoryId) {
                categoryDetails = await Category.findById(categoryId)
                    .select('title description icon color type parentCategory productCount');
            }

            // Format response with aggregation for statistics
            const priceStats = await Product.aggregate([
                { $match: query },
                {
                    $group: {
                        _id: null,
                        minPrice: { $min: '$price' },
                        maxPrice: { $max: '$price' },
                        avgPrice: { $avg: '$price' },
                        totalProducts: { $sum: 1 },
                        totalInStock: { $sum: { $cond: [{ $gt: ['$stock', 0] }, 1, 0] } }
                    }
                }
            ]);

            const stats = priceStats[0] || {
                minPrice: 0,
                maxPrice: 0,
                avgPrice: 0,
                totalProducts: 0,
                totalInStock: 0
            };

            res.json({
                success: true,
                message: 'Products fetched successfully',
                data: {
                    products,
                    category: categoryDetails,
                    pagination: {
                        page: parseInt(page),
                        limit: parseInt(limit),
                        total,
                        totalPages,
                        hasNextPage: page < totalPages,
                        hasPrevPage: page > 1
                    },
                    filters: {
                        applied: {
                            categoryId,
                            type,
                            status,
                            minPrice,
                            maxPrice,
                            search
                        },
                        stats: {
                            priceRange: {
                                min: stats.minPrice,
                                max: stats.maxPrice,
                                average: stats.avgPrice
                            },
                            count: {
                                total: stats.totalProducts,
                                inStock: stats.totalInStock,
                                outOfStock: stats.totalProducts - stats.totalInStock
                            }
                        }
                    }
                }
            });
        } catch (error) {
            console.error('Get products by category error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to fetch products by category'
            });
        }
    }

    // Get products by category tree (including subcategories)
    async getProductsByCategoryTree(req, res) {
        try {
            const { categoryId } = req.params;
            const { includeSubcategories = true } = req.query;

            // Get category and its subcategories if requested
            let categoryIds = [categoryId];

            if (includeSubcategories === 'true') {
                const categoryTree = await Category.find({
                    $or: [
                        { _id: categoryId },
                        { parentCategory: categoryId }
                    ]
                });
                categoryIds = categoryTree.map(cat => cat._id);
            }

            // Get products in these categories
            const products = await Product.find({
                category: { $in: categoryIds },
                status: 'active'
            })
                .populate('category', 'title icon color')
                .sort({ createdAt: -1 })
                .lean();

            // Get category details
            const mainCategory = await Category.findById(categoryId)
                .select('title description icon color type parentCategory productCount')
                .lean();

            // Group products by subcategory
            const productsBySubcategory = {};
            const allProducts = [];

            for (const product of products) {
                allProducts.push(product);

                if (product.category) {
                    const catId = product.category._id.toString();
                    if (!productsBySubcategory[catId]) {
                        productsBySubcategory[catId] = {
                            category: product.category,
                            products: []
                        };
                    }
                    productsBySubcategory[catId].products.push(product);
                }
            }

            res.json({
                success: true,
                message: 'Products fetched successfully',
                data: {
                    mainCategory,
                    products: allProducts,
                    groupedBySubcategory: productsBySubcategory,
                    statistics: {
                        totalProducts: allProducts.length,
                        totalSubcategories: Object.keys(productsBySubcategory).length,
                        productsByStatus: {
                            inStock: allProducts.filter(p => p.stock > 0).length,
                            outOfStock: allProducts.filter(p => p.stock === 0).length
                        }
                    }
                }
            });
        } catch (error) {
            console.error('Get products by category tree error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to fetch products by category tree'
            });
        }
    }

    // Get marketplace products (public endpoint for marketplace)
    async getMarketplaceProducts(req, res) {
        try {
            const {
                category,
                subcategory,
                type,
                location,
                minPrice,
                maxPrice,
                sort = 'recent',
                page = 1,
                limit = 20,
                search
            } = req.query;

            // Build query
            const query = { status: 'active' };

            // Category filter
            if (category) {
                if (mongoose.Types.ObjectId.isValid(category)) {
                    query.category = category;
                } else {
                    // Search by category name
                    const categories = await Category.find({
                        title: { $regex: category, $options: 'i' }
                    });
                    if (categories.length > 0) {
                        query.category = { $in: categories.map(c => c._id) };
                    }
                }
            }

            // Type filter (Product/Service)
            if (type && ['Product', 'Service'].includes(type)) {
                query.type = type;
            }

            // Location filter
            if (location) {
                query.location = { $regex: location, $options: 'i' };
            }

            // Price range
            if (minPrice || maxPrice) {
                query.price = {};
                if (minPrice) query.price.$gte = parseFloat(minPrice);
                if (maxPrice) query.price.$lte = parseFloat(maxPrice);
            }

            // Search
            if (search) {
                query.$or = [
                    { name: { $regex: search, $options: 'i' } },
                    { description: { $regex: search, $options: 'i' } },
                    { companyName: { $regex: search, $options: 'i' } }
                ];
            }

            // Sort options
            const sortOptions = {
                recent: { createdAt: -1 },
                popular: { views: -1, sales: -1 },
                price_low: { price: 1 },
                price_high: { price: -1 },
                rating: { 'rating.average': -1, 'rating.count': -1 }
            };

            const sortQuery = sortOptions[sort] || sortOptions.recent;

            // Pagination
            const skip = (page - 1) * limit;

            // Execute query
            const [products, total] = await Promise.all([
                Product.find(query)
                    .populate('category', 'title icon color')
                    .sort(sortQuery)
                    .skip(skip)
                    .limit(parseInt(limit))
                    .lean(),
                Product.countDocuments(query)
            ]);

            // Get featured products
            const featuredProducts = await Product.find({
                ...query,
                isFeatured: true
            })
                .populate('category', 'title icon color')
                .limit(5)
                .lean();

            const totalPages = Math.ceil(total / limit);

            res.json({
                success: true,
                message: 'Marketplace products fetched successfully',
                data: {
                    products,
                    featuredProducts,
                    pagination: {
                        page: parseInt(page),
                        limit: parseInt(limit),
                        total,
                        totalPages,
                        hasNextPage: page < totalPages,
                        hasPrevPage: page > 1
                    },
                    filters: {
                        category,
                        type,
                        location,
                        priceRange: { minPrice, maxPrice },
                        sort,
                        search
                    }
                }
            });
        } catch (error) {
            console.error('Get marketplace products error:', error);
            res.status(500).json({
                success: false,
                message: error.message || 'Failed to fetch marketplace products'
            });
        }
    }
}

module.exports = new ProductController();