const express = require('express');
const router = express.Router();
const marketController = require('../controllers/marketController');
const auth  = require('../middleware/auth');

// Market overview (public)
router.get('/overview', marketController.getMarketOverview);

// Asset price history (public)
router.get('/history/:symbol', marketController.getAssetPriceHistory);

// Asset details (public)
router.get('/assets/:symbol', marketController.getAssetDetails);

// Search assets (public)
router.get('/search/:query', marketController.searchAssets);

// Routes requiring authentication
router.use(auth);

// Get watchlist
router.get('/watchlist', marketController.getWatchlist);

// Add to watchlist
router.post('/watchlist', marketController.addToWatchlist);

module.exports = router;