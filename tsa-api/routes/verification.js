const express = require('express');
const router = express.Router();
const auth = require('../middleware/auth');
const verificationController = require('../controllers/verificationController');

// Submit for manual verification
router.post('/submit', auth, verificationController.submitForVerification);

// Get verification status
router.get('/status', auth, verificationController.getVerificationStatus);

// Verify BVN (for user)
router.post('/bvn/verify', auth, verificationController.verifyBVN);

// Verify document (OCR)
router.post('/document/verify', auth, verificationController.verifyDocument);

// Facial recognition verification
router.post('/facial/verify', auth, verificationController.verifyFacialImages);

// Get verification history
router.get('/history', auth, verificationController.getVerificationHistory);

// Resend verification email
router.post('/resend-email', auth, verificationController.resendVerificationEmail);

// Admin routes (protected by admin middleware)
router.get('/admin/pending', auth, verificationController.getPendingVerifications);
router.get('/admin/:userId', auth, verificationController.getUserVerificationDetails);
router.post('/admin/:userId/approve', auth, verificationController.approveVerification);
router.post('/admin/:userId/reject', auth, verificationController.rejectVerification);
router.post('/admin/:userId/request-more-info', auth, verificationController.requestMoreInfo);

module.exports = router;