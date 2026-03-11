const Transaction = require('../models/Transaction');
const Asset = require('../models/Asset');
const Wallet = require('../models/Wallet');
const blockchainService = require('../services/blockchainService');

class TransactionController {
  // Get user transactions
  async getTransactions(req, res) {
    try {
      const userId = req.user._id;
      const {
        page = 1,
        limit = 20,
        type,
        status,
        asset,
        startDate,
        endDate
      } = req.query;

      // Build query
      const query = { userId };
      
      if (type) query.type = type;
      if (status) query.status = status;
      if (asset) {
        query.$or = [
          { 'fromAsset.symbol': asset },
          { 'toAsset.symbol': asset }
        ];
      }
      
      if (startDate || endDate) {
        query.createdAt = {};
        if (startDate) query.createdAt.$gte = new Date(startDate);
        if (endDate) query.createdAt.$lte = new Date(endDate);
      }

      // Pagination
      const skip = (page - 1) * limit;

      const [transactions, total] = await Promise.all([
        Transaction.find(query)
          .sort({ createdAt: -1 })
          .skip(skip)
          .limit(parseInt(limit))
          .lean(),
        Transaction.countDocuments(query)
      ]);

      // Enrich with asset details
      const enrichedTransactions = await Promise.all(
        transactions.map(async (tx) => {
          const fromAsset = tx.fromAsset?.symbol ? 
            await Asset.findOne({ userId, symbol: tx.fromAsset.symbol }) : null;
          const toAsset = tx.toAsset?.symbol ? 
            await Asset.findOne({ userId, symbol: tx.toAsset.symbol }) : null;

          return {
            ...tx,
            fromAssetDetails: fromAsset,
            toAssetDetails: toAsset
          };
        })
      );

      res.json({
        success: true,
        data: {
          transactions: enrichedTransactions,
          pagination: {
            page: parseInt(page),
            limit: parseInt(limit),
            total,
            pages: Math.ceil(total / limit)
          }
        }
      });
    } catch (error) {
      console.error('Get transactions error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch transactions'
      });
    }
  }

  // Create deposit transaction
  async createDeposit(req, res) {
    try {
      const userId = req.user._id;
      const { assetSymbol, amount, fromAddress, transactionHash } = req.body;

      if (!assetSymbol || !amount || amount <= 0) {
        return res.status(400).json({
          success: false,
          message: 'Invalid deposit details'
        });
      }

      // Get or create asset
      let asset = await Asset.findOne({ userId, symbol: assetSymbol.toUpperCase() });
      if (!asset) {
        // Create new asset if it doesn't exist
        asset = await Asset.create({
          userId,
          symbol: assetSymbol.toUpperCase(),
          name: assetSymbol.toUpperCase(),
          balance: 0,
          usdValue: 0,
          details: {
            type: 'token',
            chain: 'ethereum'
          }
        });
      }

      // Get current price
      const price = await this.getAssetPrice(assetSymbol);
      const usdValue = amount * price;

      // Create deposit transaction
      const transaction = await Transaction.create({
        userId,
        type: 'deposit',
        status: 'pending',
        fromAsset: {
          symbol: assetSymbol,
          amount,
          address: fromAddress
        },
        toAsset: {
          symbol: assetSymbol,
          amount,
          address: this.getWalletAddress(userId, 'ethereum')
        },
        amount,
        usdValue,
        transactionHash,
        blockchain: 'ethereum',
        metadata: {
          senderAddress: fromAddress,
          receiverAddress: this.getWalletAddress(userId, 'ethereum')
        }
      });

      // If transaction hash provided, mark as processing
      if (transactionHash) {
        transaction.status = 'processing';
        await transaction.save();

        // Verify blockchain transaction
        setTimeout(async () => {
          await this.verifyBlockchainTransaction(transaction._id, transactionHash);
        }, 1000);
      }

      res.status(201).json({
        success: true,
        message: 'Deposit initiated',
        data: transaction
      });
    } catch (error) {
      console.error('Create deposit error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to create deposit'
      });
    }
  }

  // Create withdrawal transaction
  async createWithdrawal(req, res) {
    try {
      const userId = req.user._id;
      const { assetSymbol, amount, toAddress, network = 'ethereum' } = req.body;

      if (!assetSymbol || !amount || amount <= 0 || !toAddress) {
        return res.status(400).json({
          success: false,
          message: 'Invalid withdrawal details'
        });
      }

      // Check asset balance
      const asset = await Asset.findOne({ userId, symbol: assetSymbol.toUpperCase() });
      if (!asset || asset.balance < amount) {
        return res.status(400).json({
          success: false,
          message: 'Insufficient balance'
        });
      }

      // Check withdrawal limits
      const canWithdraw = await this.checkWithdrawalLimits(userId, amount, assetSymbol);
      if (!canWithdraw.allowed) {
        return res.status(400).json({
          success: false,
          message: canWithdraw.message
        });
      }

      // Get current price
      const price = await this.getAssetPrice(assetSymbol);
      const usdValue = amount * price;

      // Calculate fees
      const fees = await this.calculateWithdrawalFees(assetSymbol, network, amount);

      // Create withdrawal transaction
      const transaction = await Transaction.create({
        userId,
        type: 'withdrawal',
        status: 'pending',
        fromAsset: {
          symbol: assetSymbol,
          amount,
          address: this.getWalletAddress(userId, network)
        },
        toAsset: {
          symbol: assetSymbol,
          amount: amount - fees.total,
          address: toAddress
        },
        amount,
        usdValue,
        fees: {
          network: fees.network,
          platform: fees.platform
        },
        blockchain: network,
        metadata: {
          senderAddress: this.getWalletAddress(userId, network),
          receiverAddress: toAddress
        }
      });

      // Update asset balance immediately
      asset.balance -= amount;
      await asset.save();

      // Process withdrawal (in production, this would call blockchain service)
      setTimeout(async () => {
        await this.processWithdrawal(transaction._id);
      }, 1000);

      res.status(201).json({
        success: true,
        message: 'Withdrawal initiated',
        data: transaction
      });
    } catch (error) {
      console.error('Create withdrawal error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to create withdrawal'
      });
    }
  }

  // Create swap transaction
  async createSwap(req, res) {
    try {
      const userId = req.user._id;
      const { fromAssetSymbol, toAssetSymbol, amount } = req.body;

      if (!fromAssetSymbol || !toAssetSymbol || !amount || amount <= 0) {
        return res.status(400).json({
          success: false,
          message: 'Invalid swap details'
        });
      }

      // Check if same asset
      if (fromAssetSymbol.toUpperCase() === toAssetSymbol.toUpperCase()) {
        return res.status(400).json({
          success: false,
          message: 'Cannot swap same asset'
        });
      }

      // Check from asset balance
      const fromAsset = await Asset.findOne({ 
        userId, 
        symbol: fromAssetSymbol.toUpperCase() 
      });
      
      if (!fromAsset || fromAsset.balance < amount) {
        return res.status(400).json({
          success: false,
          message: 'Insufficient balance'
        });
      }

      // Get or create to asset
      let toAsset = await Asset.findOne({ 
        userId, 
        symbol: toAssetSymbol.toUpperCase() 
      });
      
      if (!toAsset) {
        toAsset = await Asset.create({
          userId,
          symbol: toAssetSymbol.toUpperCase(),
          name: toAssetSymbol.toUpperCase(),
          balance: 0,
          usdValue: 0,
          details: {
            type: 'token',
            chain: 'ethereum'
          }
        });
      }

      // Get exchange rate
      const fromPrice = await this.getAssetPrice(fromAssetSymbol);
      const toPrice = await this.getAssetPrice(toAssetSymbol);
      
      if (!fromPrice || !toPrice) {
        return res.status(400).json({
          success: false,
          message: 'Unable to get asset prices'
        });
      }

      const exchangeRate = toPrice / fromPrice;
      const receivedAmount = amount * exchangeRate;

      // Calculate fees (0.1% platform fee)
      const platformFee = amount * 0.001;
      const finalAmount = amount - platformFee;
      const finalReceivedAmount = finalAmount * exchangeRate;

      // Get USD values
      const fromUsdValue = finalAmount * fromPrice;
      const toUsdValue = finalReceivedAmount * toPrice;

      // Create swap transaction
      const transaction = await Transaction.create({
        userId,
        type: 'swap',
        status: 'pending',
        fromAsset: {
          symbol: fromAssetSymbol,
          amount: finalAmount
        },
        toAsset: {
          symbol: toAssetSymbol,
          amount: finalReceivedAmount
        },
        amount: finalAmount,
        usdValue: fromUsdValue,
        fees: {
          platform: {
            amount: platformFee,
            asset: fromAssetSymbol,
            usdValue: platformFee * fromPrice
          }
        },
        metadata: {
          exchangeRate,
          platformFee,
          originalAmount: amount
        }
      });

      // Update asset balances
      fromAsset.balance -= amount;
      await fromAsset.save();

      toAsset.balance += finalReceivedAmount;
      await toAsset.save();

      // Complete transaction
      transaction.status = 'completed';
      transaction.completedAt = new Date();
      await transaction.save();

      res.status(201).json({
        success: true,
        message: 'Swap completed successfully',
        data: transaction
      });
    } catch (error) {
      console.error('Create swap error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to create swap'
      });
    }
  }

  // Get transaction details
  async getTransactionDetails(req, res) {
    try {
      const { transactionId } = req.params;
      const userId = req.user._id;

      const transaction = await Transaction.findOne({
        _id: transactionId,
        userId
      });

      if (!transaction) {
        return res.status(404).json({
          success: false,
          message: 'Transaction not found'
        });
      }

      // Get blockchain confirmation if applicable
      let blockchainConfirmation = null;
      if (transaction.transactionHash && transaction.blockchain !== 'internal') {
        blockchainConfirmation = await blockchainService.getTransactionReceipt(
          transaction.transactionHash,
          transaction.blockchain
        );
      }

      res.json({
        success: true,
        data: {
          transaction,
          blockchainConfirmation
        }
      });
    } catch (error) {
      console.error('Get transaction details error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch transaction details'
      });
    }
  }

  // Get transaction stats
  async getTransactionStats(req, res) {
    try {
      const userId = req.user._id;
      const { period = 'month' } = req.query;

      let startDate;
      const endDate = new Date();

      switch (period) {
        case 'day':
          startDate = new Date(endDate);
          startDate.setDate(startDate.getDate() - 1);
          break;
        case 'week':
          startDate = new Date(endDate);
          startDate.setDate(startDate.getDate() - 7);
          break;
        case 'month':
          startDate = new Date(endDate);
          startDate.setMonth(startDate.getMonth() - 1);
          break;
        case 'year':
          startDate = new Date(endDate);
          startDate.setFullYear(startDate.getFullYear() - 1);
          break;
        default:
          startDate = new Date(endDate);
          startDate.setMonth(startDate.getMonth() - 1);
      }

      // Aggregate transaction stats
      const stats = await Transaction.aggregate([
        {
          $match: {
            userId: mongoose.Types.ObjectId(userId),
            status: 'completed',
            createdAt: { $gte: startDate, $lte: endDate }
          }
        },
        {
          $group: {
            _id: '$type',
            count: { $sum: 1 },
            totalAmount: { $sum: '$amount' },
            totalUsdValue: { $sum: '$usdValue' }
          }
        },
        {
          $project: {
            type: '$_id',
            count: 1,
            totalAmount: 1,
            totalUsdValue: 1,
            averageAmount: { $divide: ['$totalAmount', '$count'] },
            _id: 0
          }
        }
      ]);

      // Calculate totals
      const totals = {
        count: stats.reduce((sum, stat) => sum + stat.count, 0),
        amount: stats.reduce((sum, stat) => sum + stat.totalAmount, 0),
        usdValue: stats.reduce((sum, stat) => sum + stat.totalUsdValue, 0)
      };

      res.json({
        success: true,
        data: {
          stats,
          totals,
          period,
          startDate,
          endDate
        }
      });
    } catch (error) {
      console.error('Get transaction stats error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch transaction stats'
      });
    }
  }

  // Helper methods
  async getAssetPrice(symbol) {
    const prices = await priceService.getPrices([symbol]);
    return prices[symbol]?.usd || 0;
  }

  getWalletAddress(userId, chain) {
    // In production, retrieve from Wallet model
    return `0x${userId.toString().slice(-40)}`;
  }

  async checkWithdrawalLimits(userId, amount, assetSymbol) {
    const wallet = await Wallet.findOne({ userId });
    if (!wallet) {
      return { allowed: false, message: 'Wallet not found' };
    }

    const price = await this.getAssetPrice(assetSymbol);
    const usdValue = amount * price;

    // Check daily limit
    const today = new Date();
    today.setHours(0, 0, 0, 0);

    const dailyWithdrawals = await Transaction.aggregate([
      {
        $match: {
          userId: mongoose.Types.ObjectId(userId),
          type: 'withdrawal',
          status: 'completed',
          createdAt: { $gte: today }
        }
      },
      {
        $group: {
          _id: null,
          total: { $sum: '$usdValue' }
        }
      }
    ]);

    const dailyTotal = dailyWithdrawals[0]?.total || 0;
    if (dailyTotal + usdValue > wallet.security.dailyLimit) {
      return {
        allowed: false,
        message: `Daily withdrawal limit exceeded. Remaining: $${(wallet.security.dailyLimit - dailyTotal).toFixed(2)}`
      };
    }

    // Check transaction limit
    if (usdValue > wallet.security.transactionLimit) {
      return {
        allowed: false,
        message: `Transaction limit exceeded. Max: $${wallet.security.transactionLimit}`
      };
    }

    return { allowed: true };
  }

  async calculateWithdrawalFees(assetSymbol, network, amount) {
    // Mock fee calculation
    const price = await this.getAssetPrice(assetSymbol);
    
    const networkFee = {
      amount: 0.001, // Example: 0.001 ETH for gas
      asset: network === 'ethereum' ? 'ETH' : 'MATIC',
      usdValue: 0.001 * (await this.getAssetPrice(network === 'ethereum' ? 'ETH' : 'MATIC'))
    };

    const platformFee = {
      amount: amount * 0.005, // 0.5% platform fee
      asset: assetSymbol,
      usdValue: amount * 0.005 * price
    };

    return {
      network: networkFee,
      platform: platformFee,
      total: networkFee.amount + platformFee.amount
    };
  }

  async processWithdrawal(transactionId) {
    try {
      const transaction = await Transaction.findById(transactionId);
      if (!transaction) return;

      // In production, this would call blockchain service
      // For now, simulate processing
      setTimeout(async () => {
        transaction.status = 'completed';
        transaction.completedAt = new Date();
        await transaction.save();
      }, 5000);
    } catch (error) {
      console.error('Process withdrawal error:', error);
    }
  }

  async verifyBlockchainTransaction(transactionId, txHash) {
    try {
      const transaction = await Transaction.findById(transactionId);
      if (!transaction) return;

      // In production, verify on blockchain
      // For now, simulate verification
      setTimeout(async () => {
        transaction.status = 'completed';
        transaction.completedAt = new Date();
        await transaction.save();

        // Update asset balance
        const asset = await Asset.findOne({
          userId: transaction.userId,
          symbol: transaction.toAsset.symbol
        });

        if (asset) {
          asset.balance += transaction.amount;
          await asset.save();
        }
      }, 10000);
    } catch (error) {
      console.error('Verify blockchain transaction error:', error);
    }
  }
}

module.exports = new TransactionController();