// routes/auth.js - Authentication routes
const express = require('express');
const router = express.Router();
const { body, validationResult } = require('express-validator');
const jwt = require('jsonwebtoken');
const User = require('../models/User');
const auth = require('../middleware/auth');

// Signup endpoint for Screen 1
router.post('/signup', [
  body('name').trim().notEmpty().withMessage('Name is required'),
  body('username').trim().notEmpty().withMessage('Username is required'),
  body('email').isEmail().withMessage('Please enter a valid email'),
  body('password')
    .isLength({ min: 8 }).withMessage('Password must be at least 8 characters')
    .matches(/[A-Z]/).withMessage('Password must contain at least one uppercase letter')
    .matches(/[a-z]/).withMessage('Password must contain at least one lowercase letter')
    .matches(/\d/).withMessage('Password must contain at least one number'),
  body('confirmPassword').custom((value, { req }) => {
    if (value !== req.body.password) {
      throw new Error('Passwords do not match');
    }
    return true;
  }),
  body('phoneNumber').notEmpty().withMessage('Phone number is required'),
  body('country').notEmpty().withMessage('Country is required'),
  body('address').notEmpty().withMessage('Address is required')
], async (req, res) => {
  try {
    // Validate request
    const errors = validationResult(req);
    if (!errors.isEmpty()) {
      return res.status(400).json({
        success: false,
        errors: errors.array()
      });
    }

    // Check if user already exists
    const existingUser = await User.findOne({
      $or: [
        { email: req.body.email.toLowerCase() },
        { username: req.body.username.toLowerCase() },
        { phoneNumber: req.body.phoneNumber }
      ]
    });

    if (existingUser) {
      const errors = [];
      if (existingUser.email === req.body.email.toLowerCase()) {
        errors.push({ field: 'email', message: 'Email already registered' });
      }
      if (existingUser.username === req.body.username.toLowerCase()) {
        errors.push({ field: 'username', message: 'Username already taken' });
      }
      if (existingUser.phoneNumber === req.body.phoneNumber) {
        errors.push({ field: 'phoneNumber', message: 'Phone number already registered' });
      }

      return res.status(400).json({
        success: false,
        errors
      });
    }

    // Check referral code if provided
    let referredBy = null;
    if (req.body.referralCode) {
      const referrer = await User.findOne({
        referralCode: req.body.referralCode,
        accountStatus: 'active'
      });

      if (referrer) {
        referredBy = referrer._id;
      }
    }

    // Generate unique referral code for new user
    const generateReferralCode = () => {
      return Math.random().toString(36).substring(2, 8).toUpperCase() +
        Math.random().toString(36).substring(2, 4).toUpperCase();
    };

    // Create new user
    const user = new User({
      name: req.body.name,
      username: req.body.username.toLowerCase(),
      email: req.body.email.toLowerCase(),
      password: req.body.password,
      phoneNumber: req.body.phoneNumber,
      country: req.body.country,
      state: req.body.state,
      city: req.body.city,
      address: req.body.address,
      role: 'user',
      referralCode: req.body.username.toLowerCase(),
      referredBy: referredBy,
      profilePhoto: req.body.profilePhoto ? {
        url: req.body.profilePhoto,
        publicId: null // Will be set when uploaded to Cloudinary
      } : null
    });

    await user.save();

    // Generate JWT token
    const token = jwt.sign(
      { userId: user._id, email: user.email },
      process.env.JWT_SECRET,
      { expiresIn: '7d' }
    );

    res.status(201).json({
      success: true,
      message: 'Registration successful. Please proceed to identity verification.',
      data: {
        userId: user._id,
        token,
        nextStep: 'identity_verification'
      }
    });

  } catch (error) {
    console.error('Signup error:', error);
    res.status(500).json({
      success: false,
      message: 'Internal server error',
      error: error.message
    });
  }
});

// Update identity documents (Screen 2)
router.post('/identity', auth, async (req, res) => {
  try {
    const userId = req.user._id;
    const updateData = {};

    // Update documents if provided
    if (req.body.driversLicenseFront) {
      updateData['documents.driversLicense.front.url'] = req.body.driversLicenseFront;
      updateData['documents.driversLicense.front.verified'] = false;
    }
    if (req.body.driversLicenseBack) {
      updateData['documents.driversLicense.back.url'] = req.body.driversLicenseBack;
      updateData['documents.driversLicense.back.verified'] = false;
    }
    if (req.body.ninFront) {
      updateData['documents.nin.front.url'] = req.body.ninFront;
      updateData['documents.nin.front.verified'] = false;
    }
    if (req.body.ninBack) {
      updateData['documents.nin.back.url'] = req.body.ninBack;
      updateData['documents.nin.back.verified'] = false;
    }
    if (req.body.passportPhoto) {
      updateData['documents.passport.photo.url'] = req.body.passportPhoto;
      updateData['documents.passport.photo.verified'] = false;
    }
    if (req.body.pvcCard) {
      updateData['documents.pvc.card.url'] = req.body.pvcCard;
      updateData['documents.pvc.card.verified'] = false;
    }
    if (req.body.bvn) {
      updateData['documents.bvn.number'] = req.body.bvn;
      updateData['documents.bvn.verified'] = false;
    }

    const user = await User.findByIdAndUpdate(
      userId,
      { $set: updateData },
      { new: true, runValidators: true }
    );

    if (!user) {
      return res.status(404).json({
        success: false,
        message: 'User not found'
      });
    }

    res.json({
      success: true,
      message: 'Identity documents updated successfully',
      data: {
        userId: user._id,
        nextStep: 'facial_verification',
        documents: user.documents
      }
    });

  } catch (error) {
    console.error('Identity update error:', error);
    res.status(500).json({
      success: false,
      message: 'Internal server error',
      error: error.message
    });
  }
});

// Update facial verification (Screen 3)
router.post('/facial', auth, async (req, res) => {
  try {
    const userId = req.user._id;
    const updateData = {};

    // Update facial verification images
    if (req.body.faceFront) {
      updateData['facialVerification.faceFront.url'] = req.body.faceFront;
    }
    if (req.body.faceLeft) {
      updateData['facialVerification.faceLeft.url'] = req.body.faceLeft;
    }
    if (req.body.faceRight) {
      updateData['facialVerification.faceRight.url'] = req.body.faceRight;
    }
    if (req.body.faceUp) {
      updateData['facialVerification.faceUp.url'] = req.body.faceUp;
    }
    if (req.body.faceDown) {
      updateData['facialVerification.faceDown.url'] = req.body.faceDown;
    }

    // Mark as pending verification
    updateData['verificationStatus'] = 'in_review';

    const user = await User.findByIdAndUpdate(
      userId,
      { $set: updateData },
      { new: true, runValidators: true }
    );

    if (!user) {
      return res.status(404).json({
        success: false,
        message: 'User not found'
      });
    }

    // Trigger verification process (can be handled by a background job)
    await triggerVerificationProcess(userId);

    res.json({
      success: true,
      message: 'Facial verification images uploaded successfully. Verification is in progress.',
      data: {
        userId: user._id,
        verificationStatus: user.verificationStatus,
        estimatedTime: '24-48 hours'
      }
    });

  } catch (error) {
    console.error('Facial verification error:', error);
    res.status(500).json({
      success: false,
      message: 'Internal server error',
      error: error.message
    });
  }
});

// Trigger verification process (simplified)
async function triggerVerificationProcess(userId) {
  // This would be a more complex process involving:
  // 1. Document verification service
  // 2. Facial recognition comparison
  // 3. BVN verification with financial institutions
  // 4. Manual review by admin team

  console.log(`Verification process triggered for user: ${userId}`);

  // For now, we'll simulate a background process
  setTimeout(async () => {
    try {
      await User.findByIdAndUpdate(userId, {
        $set: {
          'verificationStatus': 'verified',
          'documents.driversLicense.front.verified': true,
          'documents.driversLicense.back.verified': true,
          'documents.nin.front.verified': true,
          'documents.nin.back.verified': true,
          'documents.passport.photo.verified': true,
          'documents.pvc.card.verified': true,
          'documents.bvn.verified': true,
          'documents.bvn.verifiedAt': new Date(),
          'facialVerification.verified': true,
          'facialVerification.verifiedAt': new Date(),
          'facialVerification.verificationScore': 0.95
        }
      });

      // Send verification completion email
      await sendVerificationEmail(userId);

    } catch (error) {
      console.error('Verification process error:', error);
    }
  }, 5000); // Simulate 5-second delay
}

// Send verification email
async function sendVerificationEmail(userId) {
  // Implementation would use nodemailer or similar service
  console.log(`Verification email sent to user: ${userId}`);
}

// Login endpoint
router.post('/login', [
  body('email').isEmail().withMessage('Please enter a valid email'),
  body('password').notEmpty().withMessage('Password is required')
], async (req, res) => {
  try {
    const errors = validationResult(req);
    if (!errors.isEmpty()) {
      return res.status(400).json({
        success: false,
        errors: errors.array()
      });
    }

    const { email, password } = req.body;
    const user = await User.findOne({ email: email.toLowerCase() });

    if (!user) {
      return res.status(401).json({
        success: false,
        message: 'Invalid credentials'
      });
    }

    // Check if account is locked
    if (user.isLocked()) {
      return res.status(423).json({
        success: false,
        message: 'Account is temporarily locked. Try again later.'
      });
    }

    // Check password
    const isMatch = await user.comparePassword(password);
    if (!isMatch) {
      // Increment login attempts
      await user.incrementLoginAttempts();

      return res.status(401).json({
        success: false,
        message: 'Invalid credentials'
      });
    }

    // Reset login attempts on successful login
    await User.findByIdAndUpdate(user._id, {
      $set: { loginAttempts: 0 },
      $unset: { lockUntil: 1 }
    });

    // Update last login
    user.lastLogin = new Date();
    await user.save();

    // Generate JWT token
    const token = jwt.sign(
      { userId: user._id, email: user.email },
      process.env.JWT_SECRET,
      { expiresIn: '7d' }
    );

    res.json({
      success: true,
      message: 'Login successful',
      data: {
        userId: user._id,
        role: user.role,
        token,
        name: user.name,
        email: user.email,
        verificationStatus: user.verificationStatus,
        accountStatus: user.accountStatus
      }
    });

  } catch (error) {
    console.error('Login error:', error);
    res.status(500).json({
      success: false,
      message: 'Internal server error',
      error: error.message
    });
  }
});

module.exports = router;