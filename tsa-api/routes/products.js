const express = require('express');
const router = express.Router();
const productController = require('../controllers/productController');
const categoryController = require('../controllers/categoryController');
const adminAuth = require('../middleware/adminAuth');
const auth = require('../middleware/auth');
const multer = require('multer');

const storage = multer.memoryStorage();

const upload = multer({
    storage: storage,
    limits: { fileSize: 10 * 1024 * 1024 }, // 10MB limit
    fileFilter: function (req, file, cb) {
        const filetypes = /jpeg|jpg|png|gif|webp/;
        const mimetype = filetypes.test(file.mimetype);

        if (mimetype) {
            return cb(null, true);
        } else {
            cb(new Error('Only image files are allowed!'));
        }
    }
});

// These routes are for marketplace browsing
router.get('/public/category', productController.getProductsByCategory);
router.get('/public/category/tree/:categoryId', productController.getProductsByCategoryTree);
router.get('/public/marketplace', productController.getMarketplaceProducts);
// All routes require authentication
router.use(auth);

// Product routes
router.post('/', upload.array('images', 10), productController.createProduct);
router.get('/', productController.getUserProducts);
router.get('/stats', productController.getProductStats);
router.get('/non-featured', adminAuth, productController.getNonFeaturedProducts); // Moved here

router.get('/:productId', productController.getProductById);
router.put('/:productId', upload.array('images', 10), productController.updateProduct);
router.delete('/:productId', productController.deleteProduct);
router.patch('/:productId/stock', productController.updateStock);
router.patch('/:productId/status', productController.updateStatus);
router.post('/:productId/images', upload.array('images', 10), productController.uploadImages);
router.delete('/:productId/images/:imageId', productController.deleteImage);

// Category routes
router.get('/category/all', categoryController.getCategories);
router.get('/category/tree', categoryController.getCategoryTree);
router.get('/category/:categoryId', categoryController.getCategoryById);

// Admin only routes
router.use(adminAuth);
router.patch('/:id/featured', productController.toggleFeatured);
router.post('/category', upload.single('image'), categoryController.createCategory);
router.put('/category/:categoryId', upload.single('image'), categoryController.updateCategory);
router.delete('/category/:categoryId', categoryController.deleteCategory);

module.exports = router;