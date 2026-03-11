// routes/upload.js - File upload routes (updated)
const express = require('express');
const router = express.Router();
const multer = require('multer');
const cloudinary = require('cloudinary').v2;
const { v4: uuidv4 } = require('uuid');
const auth = require('../middleware/auth');
const sharp = require('sharp');

// Configure Cloudinary
cloudinary.config({
  cloud_name: process.env.CLOUDINARY_CLOUD_NAME,
  api_key: process.env.CLOUDINARY_API_KEY,
  api_secret: process.env.CLOUDINARY_API_SECRET
});

// Configure multer for memory storage
const storage = multer.memoryStorage();
const upload = multer({
  storage: storage,
  limits: {
    fileSize: 5 * 1024 * 1024 // 5MB limit
  },
  fileFilter: (req, file, cb) => {
    const allowedTypes = /jpeg|jpg|png|gif/;
    const mimetype = allowedTypes.test(file.mimetype);
    
    if (mimetype) {
      cb(null, true);
    } else {
      cb(new Error('Only image files are allowed (jpeg, jpg, png, gif)'));
    }
  }
});

// Helper function to optimize image
const optimizeImage = async (buffer) => {
  try {
    const optimizedImage = await sharp(buffer)
      .resize(1200, 1200, { // Max dimensions
        fit: 'inside',
        withoutEnlargement: true
      })
      .jpeg({ quality: 80 })
      .toBuffer();
    
    return optimizedImage;
  } catch (error) {
    console.error('Image optimization error:', error);
    return buffer; // Return original buffer if optimization fails
  }
};

// Upload single image
router.post('/single', auth, upload.single('image'), async (req, res) => {
  try {
    if (!req.file) {
      return res.status(400).json({
        success: false,
        message: 'No file uploaded'
      });
    }

    // Optimize image
    const optimizedBuffer = await optimizeImage(req.file.buffer);
    
    // Convert buffer to base64 for Cloudinary
    const b64 = Buffer.from(optimizedBuffer).toString('base64');
    const dataURI = "data:" + req.file.mimetype + ";base64," + b64;

    // Upload to Cloudinary
    const result = await cloudinary.uploader.upload(dataURI, {
      folder: 'signup_app',
      public_id: `img_${uuidv4().slice(0, 8)}`,
      overwrite: false,
      resource_type: 'image'
    });

    res.json({
      success: true,
      message: 'File uploaded successfully',
      data: {
        url: result.secure_url,
        publicId: result.public_id,
        format: result.format,
        size: result.bytes,
        width: result.width,
        height: result.height
      }
    });
  } catch (error) {
    console.error('Upload error:', error);
    
    // Handle specific errors
    if (error.message.includes('File too large')) {
      return res.status(413).json({
        success: false,
        message: 'File is too large. Maximum size is 5MB.'
      });
    }
    
    if (error.message.includes('Only image files')) {
      return res.status(400).json({
        success: false,
        message: 'Only image files are allowed (jpeg, jpg, png, gif)'
      });
    }
    
    res.status(500).json({
      success: false,
      message: 'Upload failed',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

// Upload multiple images
router.post('/multiple', auth, upload.array('images', 10), async (req, res) => {
  try {
    if (!req.files || req.files.length === 0) {
      return res.status(400).json({
        success: false,
        message: 'No files uploaded'
      });
    }

    const uploadedFiles = [];
    const errors = [];

    // Process each file
    for (const file of req.files) {
      try {
        // Optimize image
        const optimizedBuffer = await optimizeImage(file.buffer);
        
        // Convert buffer to base64
        const b64 = Buffer.from(optimizedBuffer).toString('base64');
        const dataURI = "data:" + file.mimetype + ";base64," + b64;

        // Upload to Cloudinary
        const result = await cloudinary.uploader.upload(dataURI, {
          folder: 'signup_app/multiple',
          public_id: `img_${uuidv4().slice(0, 8)}`,
          overwrite: false,
          resource_type: 'image'
        });

        uploadedFiles.push({
          url: result.secure_url,
          publicId: result.public_id,
          format: result.format,
          size: result.bytes,
          width: result.width,
          height: result.height,
          originalName: file.originalname
        });
      } catch (error) {
        errors.push({
          originalName: file.originalname,
          error: error.message
        });
      }
    }

    if (uploadedFiles.length === 0) {
      return res.status(400).json({
        success: false,
        message: 'All uploads failed',
        errors: errors
      });
    }

    res.json({
      success: true,
      message: `${uploadedFiles.length} files uploaded successfully`,
      data: {
        uploaded: uploadedFiles,
        failed: errors.length > 0 ? errors : undefined
      }
    });
  } catch (error) {
    console.error('Multiple upload error:', error);
    res.status(500).json({
      success: false,
      message: 'Upload failed',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

// Upload base64 image (for mobile app)
router.post('/base64',/*  auth, */ async (req, res) => {
  try {
    const { image, fileName, folder = 'signup_app/base64' } = req.body;
    
    if (!image) {
      return res.status(400).json({
        success: false,
        message: 'No image data provided'
      });
    }

    // Validate base64 string
    if (!image.startsWith('data:image/')) {
      return res.status(400).json({
        success: false,
        message: 'Invalid base64 image data'
      });
    }

    // Upload to Cloudinary
    const result = await cloudinary.uploader.upload(image, {
      folder: folder,
      public_id: fileName || `img_${uuidv4().slice(0, 8)}`,
      overwrite: false,
      resource_type: 'image'
    });

    res.json({
      success: true,
      message: 'Image uploaded successfully',
      data: {
        url: result.secure_url,
        publicId: result.public_id,
        format: result.format,
        size: result.bytes,
        width: result.width,
        height: result.height
      }
    });
  } catch (error) {
    console.error('Base64 upload error:', error);
    
    // Handle specific Cloudinary errors
    if (error.message.includes('Invalid image file')) {
      return res.status(400).json({
        success: false,
        message: 'Invalid image data. Please provide a valid base64 image.'
      });
    }
    
    res.status(500).json({
      success: false,
      message: 'Upload failed',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

// Upload facial verification images (multiple base64)
router.post('/facial',/*  auth, */ async (req, res) => {
  try {
    const { images } = req.body; // Array of base64 images
    
    if (!images || !Array.isArray(images) || images.length === 0) {
      return res.status(400).json({
        success: false,
        message: 'Please provide an array of images'
      });
    }

    if (images.length > 10) {
      return res.status(400).json({
        success: false,
        message: 'Maximum 10 images allowed'
      });
    }

    const uploadedImages = [];
    const errors = [];

    // Upload each image
    for (let i = 0; i < images.length; i++) {
      try {
        const image = images[i];
        
        if (!image.startsWith('data:image/')) {
          errors.push({
            index: i,
            error: 'Invalid base64 image data'
          });
          continue;
        }

        const result = await cloudinary.uploader.upload(image, {
          folder: 'signup_app/facial',
          public_id: `facial_${uuidv4().slice(0, 8)}`,
          overwrite: false,
          resource_type: 'image'
        });

        uploadedImages.push({
          url: result.secure_url,
          publicId: result.public_id,
          index: i,
          size: result.bytes
        });
      } catch (error) {
        errors.push({
          index: i,
          error: error.message
        });
      }
    }

    res.json({
      success: true,
      message: `Uploaded ${uploadedImages.length} facial images`,
      data: {
        uploaded: uploadedImages,
        failed: errors.length > 0 ? errors : undefined
      }
    });
  } catch (error) {
    console.error('Facial upload error:', error);
    res.status(500).json({
      success: false,
      message: 'Facial image upload failed',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

// Delete image from Cloudinary
router.delete('/:publicId', auth, async (req, res) => {
  try {
    const { publicId } = req.params;
    
    if (!publicId) {
      return res.status(400).json({
        success: false,
        message: 'Public ID is required'
      });
    }

    const result = await cloudinary.uploader.destroy(publicId);
    
    if (result.result === 'ok') {
      res.json({
        success: true,
        message: 'Image deleted successfully'
      });
    } else {
      res.status(404).json({
        success: false,
        message: 'Image not found or already deleted'
      });
    }
  } catch (error) {
    console.error('Delete error:', error);
    res.status(500).json({
      success: false,
      message: 'Delete failed',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

// Get upload statistics (admin only)
router.get('/stats', auth, async (req, res) => {
  try {
    // Check if user is admin
    if (req.user.role !== 'admin') {
      return res.status(403).json({
        success: false,
        message: 'Access denied'
      });
    }

    // Get Cloudinary usage statistics
    const result = await cloudinary.api.usage();
    
    res.json({
      success: true,
      data: {
        usage: {
          bandwidth: result.bandwidth,
          storage: result.storage,
          transformations: result.transformations,
          objects: result.objects
        },
        limits: result.limit,
        credits: result.credits
      }
    });
  } catch (error) {
    console.error('Stats error:', error);
    res.status(500).json({
      success: false,
      message: 'Failed to get upload statistics',
      error: process.env.NODE_ENV === 'development' ? error.message : undefined
    });
  }
});

module.exports = router;