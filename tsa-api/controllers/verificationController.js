const User = require('../models/User');
const VerificationLog = require('../models/VerificationLog');
const cloudinary = require('cloudinary').v2;

class VerificationController {
  // Submit for manual verification
  async submitForVerification(req, res) {
    try {
      const userId = req.user._id;
      
      // Check if user has uploaded required documents
      const user = await User.findById(userId);
      
      if (!user) {
        return res.status(404).json({
          success: false,
          message: 'User not found'
        });
      }

      // Check minimum requirements
      const hasDocument = user.documents?.driversLicense?.front?.url ||
                         user.documents?.nin?.front?.url ||
                         user.documents?.passport?.photo?.url;
      
      const hasFacialImages = user.facialVerification?.faceFront?.url &&
                             user.facialVerification?.faceLeft?.url &&
                             user.facialVerification?.faceRight?.url;

      if (!hasDocument) {
        return res.status(400).json({
          success: false,
          message: 'Please upload at least one ID document'
        });
      }

      if (!hasFacialImages) {
        return res.status(400).json({
          success: false,
          message: 'Please complete facial verification'
        });
      }

      // Update verification status
      await User.findByIdAndUpdate(userId, {
        $set: {
          verificationStatus: 'in_review',
          submittedForVerificationAt: new Date()
        }
      });

      // Create verification log
      await VerificationLog.create({
        userId,
        action: 'submitted',
        status: 'in_review',
        notes: 'User submitted documents for verification'
      });

      // Notify admin (in real app, this would trigger email/notification)
      // await this.notifyAdminForVerification(userId);

      res.json({
        success: true,
        message: 'Verification submitted successfully. Our team will review your documents within 24-48 hours.',
        data: {
          verificationStatus: 'in_review',
          estimatedTime: '24-48 hours'
        }
      });
    } catch (error) {
      console.error('Submit verification error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to submit verification'
      });
    }
  }

  // Get verification status
  async getVerificationStatus(req, res) {
    try {
      const user = await User.findById(req.user._id)
        .select('verificationStatus documents facialVerification verificationNotes submittedForVerificationAt');

      if (!user) {
        return res.status(404).json({
          success: false,
          message: 'User not found'
        });
      }

      const status = {
        overall: user.verificationStatus,
        submittedAt: user.submittedForVerificationAt,
        notes: user.verificationNotes,
        documents: {
          driversLicense: {
            front: user.documents?.driversLicense?.front?.verified || false,
            back: user.documents?.driversLicense?.back?.verified || false
          },
          nin: {
            front: user.documents?.nin?.front?.verified || false,
            back: user.documents?.nin?.back?.verified || false
          },
          passport: user.documents?.passport?.photo?.verified || false,
          pvc: user.documents?.pvc?.card?.verified || false,
          bvn: user.documents?.bvn?.verified || false
        },
        facial: {
          verified: user.facialVerification?.verified || false,
          score: user.facialVerification?.verificationScore,
          verifiedAt: user.facialVerification?.verifiedAt
        },
        nextSteps: this.getNextSteps(user)
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

  // Verify BVN
  async verifyBVN(req, res) {
    try {
      const { bvn } = req.body;
      
      if (!bvn) {
        return res.status(400).json({
          success: false,
          message: 'BVN is required'
        });
      }

      // Validate BVN format
      if (!/^\d{11}$/.test(bvn)) {
        return res.status(400).json({
          success: false,
          message: 'BVN must be 11 digits'
        });
      }

      // Simulate BVN verification (replace with actual API call)
      const isVerified = await this.simulateBVNVerification(bvn);
      
      if (isVerified) {
        // Update user's BVN status
        await User.findByIdAndUpdate(req.user._id, {
          $set: {
            'documents.bvn.number': bvn,
            'documents.bvn.verified': true,
            'documents.bvn.verifiedAt': new Date()
          }
        });

        // Create verification log
        await VerificationLog.create({
          userId: req.user._id,
          action: 'bvn_verification',
          status: 'verified',
          notes: 'BVN verified successfully'
        });

        res.json({
          success: true,
          message: 'BVN verified successfully',
          data: {
            verified: true,
            verifiedAt: new Date()
          }
        });
      } else {
        res.status(400).json({
          success: false,
          message: 'BVN verification failed. Please check the number and try again.'
        });
      }
    } catch (error) {
      console.error('BVN verification error:', error);
      res.status(500).json({
        success: false,
        message: 'BVN verification service temporarily unavailable'
      });
    }
  }

  // Verify document using OCR
  async verifyDocument(req, res) {
    try {
      const { imageUrl, documentType } = req.body;
      
      if (!imageUrl || !documentType) {
        return res.status(400).json({
          success: false,
          message: 'Image URL and document type are required'
        });
      }

      // Simulate document verification (replace with actual OCR service)
      const verificationResult = await this.simulateDocumentVerification(imageUrl, documentType);
      
      if (verificationResult.success) {
        res.json({
          success: true,
          message: 'Document verification completed',
          data: verificationResult.data
        });
      } else {
        res.status(400).json({
          success: false,
          message: verificationResult.message || 'Document verification failed'
        });
      }
    } catch (error) {
      console.error('Document verification error:', error);
      res.status(500).json({
        success: false,
        message: 'Document verification service error'
      });
    }
  }

  // Verify facial images
  async verifyFacialImages(req, res) {
    try {
      const { images } = req.body; // Array of base64 images
      
      if (!images || !Array.isArray(images) || images.length < 5) {
        return res.status(400).json({
          success: false,
          message: 'At least 5 facial images are required'
        });
      }

      // Upload images to Cloudinary
      const uploadedImages = [];
      for (const image of images) {
        const result = await cloudinary.uploader.upload(image, {
          folder: 'facial_verification',
          transformation: [
            { width: 500, height: 500, crop: 'fill', gravity: 'face' },
            { quality: 'auto:good' }
          ]
        });
        uploadedImages.push(result.secure_url);
      }

      // Simulate facial recognition (replace with actual service)
      const facialResult = await this.simulateFacialRecognition(uploadedImages);
      
      if (facialResult.success) {
        // Update user's facial verification data
        const updateData = {
          'facialVerification.faceFront.url': uploadedImages[0],
          'facialVerification.faceLeft.url': uploadedImages[1],
          'facialVerification.faceRight.url': uploadedImages[2],
          'facialVerification.faceUp.url': uploadedImages[3],
          'facialVerification.faceDown.url': uploadedImages[4],
          'facialVerification.verified': facialResult.data.verified,
          'facialVerification.verificationScore': facialResult.data.verificationScore,
          'facialVerification.verifiedAt': facialResult.data.verified ? new Date() : null
        };

        await User.findByIdAndUpdate(req.user._id, { $set: updateData });

        // Create verification log
        await VerificationLog.create({
          userId: req.user._id,
          action: 'facial_verification',
          status: facialResult.data.verified ? 'verified' : 'failed',
          notes: facialResult.data.message,
          metadata: {
            score: facialResult.data.verificationScore,
            livenessScore: facialResult.data.livenessScore
          }
        });

        res.json({
          success: true,
          message: facialResult.data.message,
          data: facialResult.data
        });
      } else {
        res.status(400).json({
          success: false,
          message: facialResult.message
        });
      }
    } catch (error) {
      console.error('Facial verification error:', error);
      res.status(500).json({
        success: false,
        message: 'Facial verification service error'
      });
    }
  }

  // Get verification history
  async getVerificationHistory(req, res) {
    try {
      const logs = await VerificationLog.find({ userId: req.user._id })
        .sort({ createdAt: -1 })
        .limit(50);

      res.json({
        success: true,
        data: logs
      });
    } catch (error) {
      console.error('Get verification history error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get verification history'
      });
    }
  }

  // Resend verification email
  async resendVerificationEmail(req, res) {
    try {
      // In a real application, this would send a verification email
      // For now, we'll just return a success response
      
      res.json({
        success: true,
        message: 'Verification email sent successfully'
      });
    } catch (error) {
      console.error('Resend verification email error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to resend verification email'
      });
    }
  }

  // Admin: Get pending verifications
  async getPendingVerifications(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { page = 1, limit = 20 } = req.query;
      const skip = (page - 1) * limit;

      const [users, total] = await Promise.all([
        User.find({ verificationStatus: 'in_review' })
          .select('name email username phoneNumber submittedForVerificationAt documents facialVerification')
          .sort({ submittedForVerificationAt: 1 })
          .skip(skip)
          .limit(parseInt(limit)),
        User.countDocuments({ verificationStatus: 'in_review' })
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
      console.error('Get pending verifications error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get pending verifications'
      });
    }
  }

  // Admin: Get user verification details
  async getUserVerificationDetails(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { userId } = req.params;
      
      const user = await User.findById(userId)
        .select('name email username phoneNumber verificationStatus verificationNotes documents facialVerification submittedForVerificationAt');

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
      console.error('Get user verification details error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to get verification details'
      });
    }
  }

  // Admin: Approve verification
  async approveVerification(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { userId } = req.params;
      const { notes } = req.body;

      const updateData = {
        verificationStatus: 'verified',
        verificationNotes: notes || 'Verification approved',
        'documents.driversLicense.front.verified': true,
        'documents.driversLicense.back.verified': true,
        'documents.nin.front.verified': true,
        'documents.nin.back.verified': true,
        'documents.passport.photo.verified': true,
        'documents.pvc.card.verified': true,
        'documents.bvn.verified': true,
        'documents.bvn.verifiedAt': new Date(),
        'facialVerification.verified': true,
        'facialVerification.verifiedAt': new Date()
      };

      const user = await User.findByIdAndUpdate(
        userId,
        { $set: updateData },
        { new: true }
      );

      // Create verification log
      await VerificationLog.create({
        userId,
        action: 'admin_approval',
        status: 'verified',
        notes: notes || 'Verification approved by admin',
        adminId: req.user._id
      });

      // Send notification to user
      this.sendVerificationApprovalEmail(user);

      res.json({
        success: true,
        message: 'Verification approved successfully',
        data: {
          verificationStatus: 'verified'
        }
      });
    } catch (error) {
      console.error('Approve verification error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to approve verification'
      });
    }
  }

  // Admin: Reject verification
  async rejectVerification(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { userId } = req.params;
      const { reason } = req.body;

      if (!reason) {
        return res.status(400).json({
          success: false,
          message: 'Rejection reason is required'
        });
      }

      const updateData = {
        verificationStatus: 'rejected',
        verificationNotes: reason
      };

      await User.findByIdAndUpdate(userId, { $set: updateData });

      // Create verification log
      await VerificationLog.create({
        userId,
        action: 'admin_rejection',
        status: 'rejected',
        notes: reason,
        adminId: req.user._id
      });

      // Send notification to user
      this.sendVerificationRejectionEmail(userId, reason);

      res.json({
        success: true,
        message: 'Verification rejected',
        data: {
          verificationStatus: 'rejected'
        }
      });
    } catch (error) {
      console.error('Reject verification error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to reject verification'
      });
    }
  }

  // Admin: Request more information
  async requestMoreInfo(req, res) {
    try {
      // Check if user is admin
      if (req.user.role !== 'admin') {
        return res.status(403).json({
          success: false,
          message: 'Access denied'
        });
      }

      const { userId } = req.params;
      const { requestedInfo } = req.body;

      if (!requestedInfo) {
        return res.status(400).json({
          success: false,
          message: 'Requested information is required'
        });
      }

      // Update user with requested info
      await User.findByIdAndUpdate(userId, {
        $set: {
          verificationNotes: `Additional information requested: ${requestedInfo}`
        }
      });

      // Create verification log
      await VerificationLog.create({
        userId,
        action: 'info_requested',
        status: 'in_review',
        notes: `Admin requested additional information: ${requestedInfo}`,
        adminId: req.user._id
      });

      // Send notification to user
      this.sendInfoRequestEmail(userId, requestedInfo);

      res.json({
        success: true,
        message: 'Information request sent to user'
      });
    } catch (error) {
      console.error('Request more info error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to request more information'
      });
    }
  }

  // Helper methods (simulations for demo)
  async simulateBVNVerification(bvn) {
    // Simulate API call delay
    await new Promise(resolve => setTimeout(resolve, 2000));
    
    // 90% success rate for demo
    return Math.random() > 0.1;
  }

  async simulateDocumentVerification(imageUrl, documentType) {
    // Simulate processing delay
    await new Promise(resolve => setTimeout(resolve, 3000));
    
    const isValid = Math.random() > 0.2; // 80% valid
    
    return {
      success: true,
      data: {
        documentType,
        verified: isValid,
        confidence: isValid ? Math.random() * 0.3 + 0.7 : Math.random() * 0.3,
        extractedData: isValid ? this.generateMockDocumentData(documentType) : null
      }
    };
  }

  async simulateFacialRecognition(images) {
    // Simulate processing delay
    await new Promise(resolve => setTimeout(resolve, 4000));
    
    const verificationScore = Math.random() * 0.2 + 0.8; // 80-100% score
    const isMatch = verificationScore > 0.85; // 85% threshold
    
    return {
      success: true,
      data: {
        verified: isMatch,
        verificationScore,
        livenessScore: Math.random() * 0.2 + 0.8,
        anglesVerified: images.length,
        message: isMatch ? 
          'Facial verification successful' : 
          'Facial verification failed. Please retake images.'
      }
    };
  }

  generateMockDocumentData(documentType) {
    const data = {
      documentType,
      timestamp: new Date(),
      processingTime: Math.random() * 2 + 1 // 1-3 seconds
    };

    switch (documentType) {
      case 'drivers_license':
        data.licenseNumber = `DL${Math.floor(Math.random() * 1000000)}`;
        data.expiryDate = new Date(Date.now() + 365 * 24 * 60 * 60 * 1000);
        data.stateOfIssue = 'Lagos';
        break;
      case 'nin':
        data.ninNumber = Math.floor(Math.random() * 10000000000).toString();
        data.dateOfBirth = new Date(1990, 0, 1);
        break;
      case 'passport':
        data.passportNumber = `A${Math.floor(Math.random() * 10000000)}`;
        data.expiryDate = new Date(Date.now() + 3650 * 24 * 60 * 60 * 1000);
        data.nationality = 'Nigerian';
        break;
      case 'pvc':
        data.voterId = `PVC${Math.floor(Math.random() * 1000000)}`;
        data.state = 'Lagos';
        data.lga = 'Ikeja';
        break;
    }

    return data;
  }

  getNextSteps(user) {
    const steps = [];

    if (user.verificationStatus === 'pending') {
      steps.push('Submit your documents for verification');
    } else if (user.verificationStatus === 'rejected') {
      steps.push('Review the rejection reason and resubmit documents');
    }

    if (!user.documents?.bvn?.verified) {
      steps.push('Verify your BVN');
    }

    if (!user.facialVerification?.verified) {
      steps.push('Complete facial verification');
    }

    return steps;
  }

 /*  notifyAdminForVerification = async (userId) => {
  console.log(`Admin notification: User ${userId} submitted for verification`);
} */

  sendVerificationApprovalEmail(user) {
    console.log(`Email sent: Verification approved for ${user.email}`);
    // In real app, send email to user
  }

  sendVerificationRejectionEmail(userId, reason) {
    console.log(`Email sent: Verification rejected for user ${userId}. Reason: ${reason}`);
    // In real app, send email to user
  }

  sendInfoRequestEmail(userId, requestedInfo) {
    console.log(`Email sent: Additional info requested for user ${userId}. Info: ${requestedInfo}`);
    // In real app, send email to user
  }
}

module.exports = new VerificationController();