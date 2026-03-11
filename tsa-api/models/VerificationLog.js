const mongoose = require('mongoose');

const verificationLogSchema = new mongoose.Schema({
  userId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User',
    required: true
  },
  action: {
    type: String,
    required: true,
    enum: [
      'submitted',
      'bvn_verification', 
      'document_verification',
      'facial_verification',
      'admin_approval',
      'admin_rejection',
      'info_requested',
      'resubmitted'
    ]
  },
  status: {
    type: String,
    required: true,
    enum: ['pending', 'in_review', 'verified', 'rejected', 'failed']
  },
  notes: {
    type: String
  },
  metadata: {
    type: mongoose.Schema.Types.Mixed
  },
  adminId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User'
  },
  createdAt: {
    type: Date,
    default: Date.now
  }
}, {
  timestamps: true
});

// Index for faster queries
verificationLogSchema.index({ userId: 1, createdAt: -1 });
verificationLogSchema.index({ status: 1, createdAt: 1 });

module.exports = mongoose.model('VerificationLog', verificationLogSchema);