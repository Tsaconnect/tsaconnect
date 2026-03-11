const express = require('express');
const router = express.Router();
const portfolioController = require('../controllers/portfolioController');
const  auth  = require('../middleware/auth');

// All routes require authentication
router.use(auth);

// Get portfolio overview
router.get('/overview', portfolioController.getPortfolioOverview);

// Get portfolio performance
router.get('/performance', portfolioController.getPortfolioPerformance);

// Get asset allocation
router.get('/allocation', portfolioController.getAssetAllocation);

// Get portfolio analytics
router.get('/analytics', portfolioController.getPortfolioAnalytics);

// Create portfolio goal
router.post('/goals', portfolioController.createPortfolioGoal);

// Update portfolio goal
router.put('/goals/:goalId', portfolioController.updatePortfolioGoal);

module.exports = router;