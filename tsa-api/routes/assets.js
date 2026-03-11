const express = require('express');
const router = express.Router();
const assetController = require('../controllers/assetController');
const  auth  = require('../middleware/auth');

// All routes require authentication
router.use(auth);

// Get user assets
router.get('/', assetController.getUserAssets);

// Get asset details
router.get('/:assetId', assetController.getAssetDetails);

// Select asset for debit account
router.post('/select', assetController.selectAsset);

// Toggle asset visibility
router.put('/:assetId/visibility', assetController.toggleAssetVisibility);

// Refresh asset prices
router.post('/refresh', assetController.refreshAssetPrices);

// Add new asset
router.post('/', assetController.addAsset);

// Update asset balance
router.put('/:assetId/balance', assetController.updateAssetBalance);

// Get asset performance
router.get('/:assetId/performance', assetController.getAssetPerformance);

module.exports = router;