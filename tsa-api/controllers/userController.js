const User = require('../models/User');
const bcrypt = require('bcryptjs');
const cloudinary = require('cloudinary').v2;

class UserController {
  async getProfile(req, res) {
    try {
      const user = await User.findById(req.user._id).select('-password -tokens');
      
      if (!user) {
        return res.status(404).json({
          success: false,
          message: 'User not found'
        });
      }

      res.json({
        success: true,
        data: user
      });
    } catch (error) {
      console.error('Get profile error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch profile'
      });
    }
  }

  // Update user profile
  async updateProfile(req, res) {
    try {
      const updates = {};
      const allowedUpdates = ['name', 'phoneNumber', 'address', 'state', 'city'];
      
      // Filter allowed updates
      Object.keys(req.body).forEach(key => {
        if (allowedUpdates.includes(key)) {
          updates[key] = req.body[key];
        }
      });

      const user = await User.findByIdAndUpdate(
        req.user._id,
        { $set: updates },
        { new: true, runValidators: true }
      ).select('-password -tokens');

      res.json({
        success: true,
        message: 'Profile updated successfully',
        data: user
      });
    } catch (error) {
      console.error('Update profile error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to update profile'
      });
    }
  }

  // Get verification status
  async getVerificationStatus(req, res) {
    try {
      const user = await User.findById(req.user._id).select('verificationStatus documents facialVerification');
      
      const status = {
        overall: user.verificationStatus,
        documents: {
          driversLicense: user.documents?.driversLicense?.front?.verified || false,
          nin: user.documents?.nin?.front?.verified || false,
          passport: user.documents?.passport?.photo?.verified || false,
          pvc: user.documents?.pvc?.card?.verified || false,
          bvn: user.documents?.bvn?.verified || false
        },
        facial: user.facialVerification?.verified || false,
        completed: user.verificationStatus === 'verified'
      };

      res.json({
        success: true,
        data: status
      });
    } catch (error) {
      console.error('Get verification status error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get verification status'
      });
    }
  }

  // Get user documents
  async getDocuments(req, res) {
    try {
      const user = await User.findById(req.user._id).select('documents');
      
      res.json({
        success: true,
        data: user.documents
      });
    } catch (error) {
      console.error('Get documents error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get documents'
      });
    }
  }

  // Upload profile photo
  async uploadProfilePhoto(req, res) {
    try {
      const { image } = req.body;
      
      if (!image) {
        return res.status(400).json({
          success: false,
          message: 'No image provided'
        });
      }

      // Upload to Cloudinary
      const result = await cloudinary.uploader.upload(image, {
        folder: 'profile_photos',
        transformation: [
          { width: 400, height: 400, crop: 'fill', gravity: 'face' },
          { quality: 'auto:good' }
        ]
      });

      // Update user profile
      const user = await User.findByIdAndUpdate(
        req.user._id,
        {
          $set: {
            profilePhoto: {
              url: result.secure_url,
              publicId: result.public_id
            }
          }
        },
        { new: true }
      ).select('-password -tokens');

      // Delete old profile photo if exists
      if (req.user.profilePhoto?.publicId) {
        await cloudinary.uploader.destroy(req.user.profilePhoto.publicId);
      }

      res.json({
        success: true,
        message: 'Profile photo updated successfully',
        data: user.profilePhoto
      });
    } catch (error) {
      console.error('Upload profile photo error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to upload profile photo'
      });
    }
  }

  // Change password
  async changePassword(req, res) {
    try {
      const { currentPassword, newPassword } = req.body;
      
      if (!currentPassword || !newPassword) {
        return res.status(400).json({
          success: false,
          message: 'Current password and new password are required'
        });
      }

      // Verify current password
      const isMatch = await req.user.comparePassword(currentPassword);
      if (!isMatch) {
        return res.status(401).json({
          success: false,
          message: 'Current password is incorrect'
        });
      }

      // Validate new password strength
      const passwordStrength = this.validatePasswordStrength(newPassword);
      if (!passwordStrength.isValid) {
        return res.status(400).json({
          success: false,
          message: 'New password is too weak',
          details: passwordStrength.details
        });
      }

      // Update password
      req.user.password = newPassword;
      await req.user.save();

      res.json({
        success: true,
        message: 'Password changed successfully'
      });
    } catch (error) {
      console.error('Change password error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to change password'
      });
    }
  }

  // Delete account (soft delete)
  async deleteAccount(req, res) {
    try {
      const { password } = req.body;
      
      if (!password) {
        return res.status(400).json({
          success: false,
          message: 'Password is required to delete account'
        });
      }

      // Verify password
      const isMatch = await req.user.comparePassword(password);
      if (!isMatch) {
        return res.status(401).json({
          success: false,
          message: 'Incorrect password'
        });
      }

      // Soft delete by changing account status
      await User.findByIdAndUpdate(req.user._id, {
        $set: {
          accountStatus: 'deleted',
          deletedAt: new Date()
        }
      });

      res.json({
        success: true,
        message: 'Account deleted successfully'
      });
    } catch (error) {
      console.error('Delete account error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to delete account'
      });
    }
  }

  // Get referral stats
  async getReferralStats(req, res) {
    try {
      const referrals = await User.find({ referredBy: req.user._id })
        .select('name email createdAt verificationStatus')
        .sort({ createdAt: -1 });

      const stats = {
        totalReferrals: referrals.length,
        verifiedReferrals: referrals.filter(u => u.verificationStatus === 'verified').length,
        pendingReferrals: referrals.filter(u => u.verificationStatus === 'pending' || u.verificationStatus === 'in_review').length,
        referrals: referrals
      };

      res.json({
        success: true,
        data: stats
      });
    } catch (error) {
      console.error('Get referral stats error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get referral stats'
      });
    }
  }

  // Check username availability
  async checkUsernameAvailability(req, res) {
    try {
      const { username } = req.params;
      
      if (!username || username.length < 3) {
        return res.status(400).json({
          success: false,
          message: 'Username must be at least 3 characters'
        });
      }

      const existingUser = await User.findOne({ 
        username: username.toLowerCase() 
      });

      res.json({
        success: true,
        data: {
          available: !existingUser,
          suggestions: existingUser ? this.generateUsernameSuggestions(username) : []
        }
      });
    } catch (error) {
      console.error('Check username error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to check username availability'
      });
    }
  }

  // Check email availability
  async checkEmailAvailability(req, res) {
    try {
      const { email } = req.params;
      
      if (!email || !this.isValidEmail(email)) {
        return res.status(400).json({
          success: false,
          message: 'Invalid email address'
        });
      }

      const existingUser = await User.findOne({ 
        email: email.toLowerCase() 
      });

      res.json({
        success: true,
        data: {
          available: !existingUser
        }
      });
    } catch (error) {
      console.error('Check email error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to check email availability'
      });
    }
  }

  // Get user by ID (admin)
  async getUserById(req, res) {
    try {
      const { userId } = req.params;
      
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const user = await User.findById(userId)
        .select('-password -tokens');

      if (!user) {
        return res.status(404).json({
          success: false,
          message: 'User not found'
        });
      }

      res.json({
        success: true,
        data: user
      });
    } catch (error) {
      console.error('Get user by ID error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get user'
      });
    }
  }

  // Get all users (admin)
  async getAllUsers(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { 
        page = 1, 
        limit = 20, 
        status, 
        verificationStatus,
        search 
      } = req.query;

      const query = {};
      
      if (status) {
        query.accountStatus = status;
      }
      
      if (verificationStatus) {
        query.verificationStatus = verificationStatus;
      }
      
      if (search) {
        query.$or = [
          { name: { $regex: search, $options: 'i' } },
          { email: { $regex: search, $options: 'i' } },
          { username: { $regex: search, $options: 'i' } }
        ];
      }

      const skip = (page - 1) * limit;

      const [users, total] = await Promise.all([
        User.find(query)
          .select('-password -tokens')
          .sort({ createdAt: -1 })
          .skip(skip)
          .limit(parseInt(limit)),
        User.countDocuments(query)
      ]);

      res.json({
        success: true,
        data: {
          users,
          pagination: {
            page: parseInt(page),
            limit: parseInt(limit),
            total,
            pages: Math.ceil(total / limit)
          }
        }
      });
    } catch (error) {
      console.error('Get all users error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get users'
      });
    }
  }

  // Helper methods
  validatePasswordStrength(password) {
    const minLength = 8;
    const hasUpperCase = /[A-Z]/.test(password);
    const hasLowerCase = /[a-z]/.test(password);
    const hasNumbers = /\d/.test(password);
    const hasSpecialChar = /[!@#$%^&*(),.?":{}|<>]/.test(password);

    return {
      isValid: password.length >= minLength && hasUpperCase && hasLowerCase && hasNumbers,
      details: {
        length: password.length >= minLength,
        upperCase: hasUpperCase,
        lowerCase: hasLowerCase,
        numbers: hasNumbers,
        specialChar: hasSpecialChar
      }
    };
  }

  isValidEmail(email) {
    return /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(email);
  }

  generateUsernameSuggestions(username) {
    const suggestions = [];
    for (let i = 1; i <= 5; i++) {
      suggestions.push(`${username}${Math.floor(Math.random() * 1000)}`);
    }
    return suggestions;
  }
}

module.exports = new UserController();