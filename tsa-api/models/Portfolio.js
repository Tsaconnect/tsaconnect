const mongoose = require('mongoose');

const portfolioSchema = new mongoose.Schema({
  userId: {
    type: mongoose.Schema.Types.ObjectId,
    ref: 'User',
    required: true,
    unique: true
  },
  totalValue: {
    current: {
      type: Number,
      default: 0,
      min: 0
    },
    yesterday: {
      type: Number,
      default: 0,
      min: 0
    },
    lastWeek: {
      type: Number,
      default: 0,
      min: 0
    },
    lastMonth: {
      type: Number,
      default: 0,
      min: 0
    }
  },
  allocation: [{
    assetId: {
      type: mongoose.Schema.Types.ObjectId,
      ref: 'Asset'
    },
    symbol: String,
    percentage: Number,
    value: Number
  }],
  performance: {
    daily: {
      change: Number,
      percentage: Number
    },
    weekly: {
      change: Number,
      percentage: Number
    },
    monthly: {
      change: Number,
      percentage: Number
    },
    yearly: {
      change: Number,
      percentage: Number
    }
  },
  history: [{
    date: {
      type: Date,
      required: true
    },
    value: {
      type: Number,
      required: true,
      min: 0
    }
  }],
  goals: [{
    name: {
      type: String,
      required: true
    },
    targetAmount: {
      type: Number,
      required: true,
      min: 0
    },
    currentAmount: {
      type: Number,
      default: 0,
      min: 0
    },
    targetDate: Date,
    assets: [{
      symbol: String,
      percentage: Number
    }]
  }],
  lastUpdated: {
    type: Date,
    default: Date.now
  }
}, {
  timestamps: true
});

// Index for user portfolio
portfolioSchema.index({ userId: 1 });
portfolioSchema.index({ 'lastUpdated': -1 });

// Method to update portfolio value
portfolioSchema.methods.updateValue = function(newValue) {
  const oldValue = this.totalValue.current;
  
  // Update historical values
  if (this.totalValue.current !== newValue) {
    this.totalValue = {
      yesterday: this.totalValue.current,
      lastWeek: this.totalValue.lastWeek || newValue,
      lastMonth: this.totalValue.lastMonth || newValue,
      current: newValue
    };
    
    // Update performance metrics
    this.performance = {
      daily: {
        change: newValue - this.totalValue.yesterday,
        percentage: ((newValue - this.totalValue.yesterday) / this.totalValue.yesterday) * 100 || 0
      },
      weekly: {
        change: newValue - this.totalValue.lastWeek,
        percentage: ((newValue - this.totalValue.lastWeek) / this.totalValue.lastWeek) * 100 || 0
      },
      monthly: {
        change: newValue - this.totalValue.lastMonth,
        percentage: ((newValue - this.totalValue.lastMonth) / this.totalValue.lastMonth) * 100 || 0
      },
      yearly: {
        change: 0,
        percentage: 0
      }
    };
    
    // Add to history (keep last 365 days)
    this.history.push({
      date: new Date(),
      value: newValue
    });
    
    // Trim history to last 365 entries
    if (this.history.length > 365) {
      this.history = this.history.slice(-365);
    }
  }
  
  return this.save();
};

module.exports = mongoose.model('Portfolio', portfolioSchema);