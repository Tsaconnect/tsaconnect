// models/User.js - User model
const mongoose = require('mongoose');
const bcrypt = require('bcryptjs');

const userSchema = new mongoose.Schema({
  // Screen 1: Personal Information
  name: {
    type: String,
    required: [true, 'Name is required'],
    trim: true
  },
  username: {
    type: String,
    required: [true, 'Username is required'],
    unique: true,
    lowercase: true,
    trim: true,
    minlength: [3, 'Username must be at least 3 characters']
  },
  email: {
    type: String,
    required: [true, 'Email is required'],
    unique: true,
    lowercase: true,
    trim: true,
    match: [/^\w+([\.-]?\w+)*@\w+([\.-]?\w+)*(\.\w{2,3})+$/, 'Please enter a valid email']
  },
  password: {
    type: String,
    required: [true, 'Password is required'],
    minlength: [8, 'Password must be at least 8 characters']
  },
  role: {
    type: String,
    enum: ['user', 'admin', 'super_admin', 'merchant'],
    default: 'user'

  },
  phoneNumber: {
    type: String,
    required: [true, 'Phone number is required']
  },
  country: {
    type: String,
    required: [true, 'Country is required']
  },
  state: String,
  city: String,
  address: {
    type: String,
    required: [true, 'Address is required']
  },

  // Profile
  profilePhoto: {
    url: String,
    publicId: String
  },
  referralCode: String,
  referredBy: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User'
  },

  // Screen 2: Identity Verification Documents
  documents: {
    driversLicense: {
      front: { url: String, publicId: String, verified: Boolean },
      back: { url: String, publicId: String, verified: Boolean }
    },
    nin: {
      front: { url: String, publicId: String, verified: Boolean },
      back: { url: String, publicId: String, verified: Boolean }
    },
    passport: {
      photo: { url: String, publicId: String, verified: Boolean }
    },
    pvc: {
      card: { url: String, publicId: String, verified: Boolean }
    },
    bvn: {
      number: String,
      verified: Boolean,
      verifiedAt: Date
    }
  },

  // Screen 3: Facial Verification
  facialVerification: {
    faceFront: { url: String, publicId: String, embeddings: [Number] },
    faceLeft: { url: String, publicId: String, embeddings: [Number] },
    faceRight: { url: String, publicId: String, embeddings: [Number] },
    faceUp: { url: String, publicId: String, embeddings: [Number] },
    faceDown: { url: String, publicId: String, embeddings: [Number] },
    verificationScore: Number,
    verified: Boolean,
    verifiedAt: Date
  },

  // Verification Status
  verificationStatus: {
    type: String,
    enum: ['pending', 'in_review', 'verified', 'rejected'],
    default: 'pending'
  },
  verificationNotes: String,

  // Account Status
  accountStatus: {
    type: String,
    enum: ['active', 'inactive', 'suspended', 'deleted'],
    default: 'active'
  },
  // Metadata
  lastLogin: Date,
  loginAttempts: {
    type: Number,
    default: 0
  },
  lockUntil: Date,
}, {
  timestamps: true
});

// Hash password before saving - WITHOUT 'next' function
userSchema.pre('save', async function () {
  // Only hash the password if it's modified (or new)
  if (!this.isModified('password')) {
    return; // Simply return without calling next
  }

  try {
    const salt = await bcrypt.genSalt(10);
    this.password = await bcrypt.hash(this.password, salt);
    // No need to call next, Mongoose will continue automatically
  } catch (error) {
    // Throw the error - Mongoose will catch it and abort the save operation
    throw error;
  }
});

// Alternative approach: Using post-save or pre-validate
userSchema.pre('validate', async function () {
  // This runs before validation, also doesn't need 'next'
  if (this.isModified('password') && this.password.length < 8) {
    throw new Error('Password must be at least 8 characters');
  }
});

// Method to compare password
userSchema.methods.comparePassword = async function (candidatePassword) {
  return await bcrypt.compare(candidatePassword, this.password);
};

// Method to check if account is locked
userSchema.methods.isLocked = function () {
  return !!(this.lockUntil && this.lockUntil > Date.now());
};

// Increment login attempts
userSchema.methods.incrementLoginAttempts = async function () {
  // If lock has expired, reset attempts
  if (this.lockUntil && this.lockUntil < Date.now()) {
    this.loginAttempts = 1;
    this.lockUntil = undefined;
    return this.save();
  }

  // Increment login attempts
  this.loginAttempts += 1;

  // Lock account if attempts reach 5
  if (this.loginAttempts >= 5) {
    this.lockUntil = Date.now() + (2 * 60 * 60 * 1000); // Lock for 2 hours
  }

  return this.save();
};

// Virtual for full address
userSchema.virtual('fullAddress').get(function () {
  return `${this.address}, ${this.city}, ${this.state}, ${this.country}`.trim();
});

userSchema.index({ verificationStatus: 1 });

module.exports = mongoose.model('User', userSchema);