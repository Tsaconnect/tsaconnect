const express = require('express');
const router = express.Router();
const auth = require('../middleware/auth');
const userController = require('../controllers/userController');

// Get current user profile
router.get('/profile', auth, userController.getProfile);

// Update user profile
router.put('/profile', auth, userController.updateProfile);

// Get user verification status
router.get('/verification-status', auth, userController.getVerificationStatus);

// Get user documents
router.get('/documents', auth, userController.getDocuments);

// Upload profile photo
router.post('/profile-photo', auth, userController.uploadProfilePhoto);

// Change password
router.put('/change-password', auth, userController.changePassword);

// Delete account (soft delete)
router.delete('/account', auth, userController.deleteAccount);

// Get referral stats
router.get('/referral-stats', auth, userController.getReferralStats);

// Check username availability
router.get('/check-username/:username', userController.checkUsernameAvailability);

// Check email availability
router.get('/check-email/:email', userController.checkEmailAvailability);

// Get user by ID (admin only)
router.get('/:userId', auth, userController.getUserById);

// Get all users (admin only)
router.get('/', auth, userController.getAllUsers);

module.exports = router;