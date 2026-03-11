const mongoose = require('mongoose');

const transactionSchema = new mongoose.Schema({
  userId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User',
    required: true
  },
  type: {
    type: String,
    enum: ['deposit', 'withdrawal', 'transfer', 'swap', 'trade', 'reward', 'staking'],
    required: true
  },
  status: {
    type: String,
    enum: ['pending', 'completed', 'failed', 'cancelled', 'processing'],
    default: 'pending'
  },
  fromAsset: {
    symbol: String,
    amount: Number,
    address: String
  },
  toAsset: {
    symbol: String,
    amount: Number,
    address: String
  },
  amount: {
    type: Number,
    required: true,
    min: 0
  },
  usdValue: {
    type: Number,
    required: true,
    min: 0
  },
  fees: {
    network: {
      amount: Number,
      asset: String,
      usdValue: Number
    },
    platform: {
      amount: Number,
      asset: String,
      usdValue: Number
    }
  },
  transactionHash: {
    type: String,
    sparse: true
  },
  blockchain: {
    type: String,
    enum: ['ethereum', 'polygon', 'binance', 'solana', 'internal']
  },
  metadata: {
    senderAddress: String,
    receiverAddress: String,
    memo: String,
    exchangeRate: Number,
    gasUsed: Number,
    gasPrice: Number,
    confirmationBlocks: Number,
    notes: String
  },
  description: String,
  tags: [String],
  completedAt: Date,
  confirmedAt: Date
}, {
  timestamps: true
});

// Indexes for optimized queries
transactionSchema.index({ userId: 1, createdAt: -1 });
transactionSchema.index({ 'fromAsset.symbol': 1, 'toAsset.symbol': 1 });

// Pre-save hook to add description if not provided
transactionSchema.pre('save', function(next) {
  if (!this.description) {
    switch (this.type) {
      case 'deposit':
        this.description = `Deposit ${this.amount} ${this.toAsset.symbol}`;
        break;
      case 'withdrawal':
        this.description = `Withdraw ${this.amount} ${this.fromAsset.symbol}`;
        break;
      case 'swap':
        this.description = `Swap ${this.amount} ${this.fromAsset.symbol} to ${this.toAsset.amount} ${this.toAsset.symbol}`;
        break;
      case 'staking':
        this.description = `Stake ${this.amount} ${this.fromAsset.symbol}`;
        break;
      default:
        this.description = `${this.type.charAt(0).toUpperCase() + this.type.slice(1)} ${this.amount} ${this.fromAsset.symbol}`;
    }
  }
  next();
});

module.exports = mongoose.model('Transaction', transactionSchema);