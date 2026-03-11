const mongoose = require('mongoose');

const assetSchema = new mongoose.Schema({
  userId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User',
    required: true
  },
  symbol: {
    type: String,
    required: true,
    uppercase: true,
    trim: true
  },
  name: {
    type: String,
    required: true,
    trim: true
  },
  balance: {
    type: Number,
    required: true,
    default: 0,
    min: 0
  },
  usdValue: {
    type: Number,
    required: true,
    default: 0,
    min: 0
  },
  isSelected: {
    type: Boolean,
    default: false
  },
  isHidden: {
    type: Boolean,
    default: false
  },
  details: {
    type: {
      type: String,
      enum: ['token', 'stablecoin', 'gold-backed', 'nft', 'other'],
      required: true
    },
    chain: {
      type: String,
      enum: ['ethereum', 'polygon', 'binance', 'solana', 'other'],
      default: 'ethereum'
    },
    contractAddress: {
      type: String,
      lowercase: true,
      trim: true
    },
    decimals: {
      type: Number,
      default: 18
    },
    iconUrl: String,
    color: String
  },
  metadata: {
    apy: Number,
    stakedAmount: Number,
    unstakingDate: Date,
    lockPeriod: Number,
    lastRewardClaimed: Date
  },
  performance: {
    dailyChange: Number,
    weeklyChange: Number,
    monthlyChange: Number
  },
  lastSynced: {
    type: Date,
    default: Date.now
  }
}, {
  timestamps: true
});

// Compound index for user assets
assetSchema.index({ userId: 1, symbol: 1 });
assetSchema.index({ userId: 1, isSelected: 1 });
assetSchema.index({ userId: 1, isHidden: 1 });

// Virtual for formatted display
assetSchema.virtual('formattedBalance').get(function() {
  return parseFloat(this.balance.toFixed(6));
});

// Method to update USD value
assetSchema.methods.updateValue = async function(price) {
  this.usdValue = this.balance * price;
  this.lastSynced = new Date();
  return this.save();
};

module.exports = mongoose.model('Asset', assetSchema);