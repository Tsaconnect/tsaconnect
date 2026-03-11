const mongoose = require('mongoose');

const walletSchema = new mongoose.Schema({
  userId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User',
    required: true,
    unique: true
  },
  totalBalance: {
    type: Number,
    default: 0,
    min: 0
  },
  totalUSDValue: {
    type: Number,
    default: 0,
    min: 0
  },
  selectedAsset: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'Asset'
  },
  addresses: [{
    blockchain: {
      type: String,
      enum: ['ethereum', 'polygon', 'binance', 'solana'],
      required: true
    },
    address: {
      type: String,
      required: true,
      lowercase: true,
      trim: true
    },
    isActive: {
      type: Boolean,
      default: true
    },
    isExternal: {
      type: Boolean,
      default: false
    },
    lastSynced: Date,
    nonce: {
      type: Number,
      default: 0
    }
  }],
  security: {
    withdrawalWhitelist: [{
      address: String,
      name: String,
      addedAt: Date
    }],
    dailyLimit: {
      type: Number,
      default: 10000,
      min: 0
    },
    transactionLimit: {
      type: Number,
      default: 1000,
      min: 0
    },
    twoFactorEnabled: {
      type: Boolean,
      default: false
    }
  },
  settings: {
    defaultCurrency: {
      type: String,
      default: 'USD'
    },
    hideBalances: {
      type: Boolean,
      default: false
    },
    priceAlerts: {
      type: Boolean,
      default: true
    },
    autoSync: {
      type: Boolean,
      default: true
    }
  },
  lastSynced: {
    type: Date,
    default: Date.now
  }
}, {
  timestamps: true
});

// Index for quick lookup
walletSchema.index({ userId: 1 });
walletSchema.index({ 'addresses.address': 1 }, { sparse: true });

// Virtual for available balance
walletSchema.virtual('availableBalance').get(function() {
  return this.totalBalance;
});

module.exports = mongoose.model('Wallet', walletSchema);