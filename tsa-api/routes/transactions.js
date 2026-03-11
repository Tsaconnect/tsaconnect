const express = require('express');
const router = express.Router();
const transactionController = require('../controllers/transactionController');
const  auth  = require('../middleware/auth');

// All routes require authentication
router.use(auth);

// Get transactions
router.get('/', transactionController.getTransactions);

// Get transaction details
router.get('/:transactionId', transactionController.getTransactionDetails);

// Get transaction stats
router.get('/stats/summary', transactionController.getTransactionStats);

// Create deposit
router.post('/deposit', transactionController.createDeposit);

// Create withdrawal
router.post('/withdraw', transactionController.createWithdrawal);

// Create swap
router.post('/swap', transactionController.createSwap);

module.exports = router;