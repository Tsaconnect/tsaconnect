const Portfolio = require('../models/Portfolio');
const Asset = require('../models/Asset');
const Transaction = require('../models/Transaction');
const priceService = require('../services/priceService');

class PortfolioController {
  // Get portfolio overview
  async getPortfolioOverview(req, res) {
    try {
      const userId = req.user._id;

      // Get or create portfolio
      let portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        portfolio = await Portfolio.create({ userId });
      }

      // Get assets for allocation
      const assets = await Asset.find({ userId, isHidden: false });
      const totalValue = assets.reduce((sum, asset) => sum + asset.usdValue, 0);

      // Update portfolio if needed
      if (portfolio.totalValue.current !== totalValue) {
        await portfolio.updateValue(totalValue);
        portfolio = await Portfolio.findOne({ userId }); // Refresh
      }

      // Calculate allocation
      const allocation = assets.map(asset => ({
        assetId: asset._id,
        symbol: asset.symbol,
        percentage: totalValue > 0 ? (asset.usdValue / totalValue) * 100 : 0,
        value: asset.usdValue,
        balance: asset.balance
      }));

      // Get recent transactions
      const recentTransactions = await Transaction.find({ userId })
        .sort({ createdAt: -1 })
        .limit(5)
        .lean();

      // Get top performing assets
      const topPerformers = await this.getTopPerformingAssets(userId);

      res.json({
        success: true,
        data: {
          portfolio,
          allocation,
          summary: {
            totalValue,
            assetCount: assets.length,
            dailyChange: portfolio.performance.daily.change,
            dailyPercentage: portfolio.performance.daily.percentage
          },
          recentTransactions,
          topPerformers,
          lastUpdated: new Date()
        }
      });
    } catch (error) {
      console.error('Get portfolio overview error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch portfolio overview'
      });
    }
  }

  // Get portfolio performance
  async getPortfolioPerformance(req, res) {
    try {
      const userId = req.user._id;
      const { period = 'month' } = req.query;

      const portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        return res.status(404).json({
          success: false,
          message: 'Portfolio not found'
        });
      }

      // Filter history based on period
      let filteredHistory = portfolio.history;
      const now = new Date();
      let startDate;

      switch (period) {
        case 'week':
          startDate = new Date(now.setDate(now.getDate() - 7));
          filteredHistory = portfolio.history.filter(h => h.date >= startDate);
          break;
        case 'month':
          startDate = new Date(now.setMonth(now.getMonth() - 1));
          filteredHistory = portfolio.history.filter(h => h.date >= startDate);
          break;
        case 'year':
          startDate = new Date(now.setFullYear(now.getFullYear() - 1));
          filteredHistory = portfolio.history.filter(h => h.date >= startDate);
          break;
        case 'all':
        default:
          filteredHistory = portfolio.history;
      }

      // Calculate performance metrics
      const performance = {
        current: portfolio.totalValue.current,
        change: portfolio.performance[period]?.change || 0,
        percentage: portfolio.performance[period]?.percentage || 0,
        history: filteredHistory,
        highs: {
          allTime: Math.max(...portfolio.history.map(h => h.value)),
          [period]: Math.max(...filteredHistory.map(h => h.value))
        },
        lows: {
          allTime: Math.min(...portfolio.history.map(h => h.value)),
          [period]: Math.min(...filteredHistory.map(h => h.value))
        }
      };

      res.json({
        success: true,
        data: performance
      });
    } catch (error) {
      console.error('Get portfolio performance error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch portfolio performance'
      });
    }
  }

  // Get asset allocation
  async getAssetAllocation(req, res) {
    try {
      const userId = req.user._id;

      const assets = await Asset.find({ userId, isHidden: false });
      const totalValue = assets.reduce((sum, asset) => sum + asset.usdValue, 0);

      const allocation = assets.map(asset => ({
        asset: {
          id: asset._id,
          symbol: asset.symbol,
          name: asset.name,
          color: asset.details.color
        },
        balance: asset.balance,
        value: asset.usdValue,
        percentage: totalValue > 0 ? (asset.usdValue / totalValue) * 100 : 0,
        performance: asset.performance || {}
      }));

      // Sort by value descending
      allocation.sort((a, b) => b.value - a.value);

      // Group by type if needed
      const byType = allocation.reduce((acc, item) => {
        const type = item.asset.symbol === 'USDT' || item.asset.symbol === 'USDC' ? 
          'Stablecoins' : item.asset.symbol === 'MCGP' ? 'Gold-Backed' : 'Tokens';
        
        if (!acc[type]) acc[type] = { total: 0, assets: [] };
        acc[type].total += item.value;
        acc[type].assets.push(item);
        return acc;
      }, {});

      res.json({
        success: true,
        data: {
          allocation,
          byType,
          totalValue
        }
      });
    } catch (error) {
      console.error('Get asset allocation error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch asset allocation'
      });
    }
  }

  // Create portfolio goal
  async createPortfolioGoal(req, res) {
    try {
      const userId = req.user._id;
      const { name, targetAmount, targetDate, assets } = req.body;

      if (!name || !targetAmount || targetAmount <= 0) {
        return res.status(400).json({
          success: false,
          message: 'Invalid goal details'
        });
      }

      const portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        return res.status(404).json({
          success: false,
          message: 'Portfolio not found'
        });
      }

      // Calculate current amount from assets
      let currentAmount = 0;
      if (assets && assets.length > 0) {
        const userAssets = await Asset.find({ userId });
        assets.forEach(targetAsset => {
          const userAsset = userAssets.find(a => a.symbol === targetAsset.symbol);
          if (userAsset) {
            const assetValue = userAsset.usdValue * (targetAsset.percentage / 100);
            currentAmount += assetValue;
          }
        });
      }

      const goal = {
        name,
        targetAmount,
        currentAmount,
        targetDate: targetDate ? new Date(targetDate) : null,
        assets: assets || [],
        createdAt: new Date()
      };

      portfolio.goals.push(goal);
      await portfolio.save();

      res.status(201).json({
        success: true,
        message: 'Portfolio goal created',
        data: goal
      });
    } catch (error) {
      console.error('Create portfolio goal error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to create portfolio goal'
      });
    }
  }

  // Update portfolio goal
  async updatePortfolioGoal(req, res) {
    try {
      const userId = req.user._id;
      const { goalId } = req.params;
      const updates = req.body;

      const portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        return res.status(404).json({
          success: false,
          message: 'Portfolio not found'
        });
      }

      const goalIndex = portfolio.goals.findIndex(g => g._id.toString() === goalId);
      if (goalIndex === -1) {
        return res.status(404).json({
          success: false,
          message: 'Goal not found'
        });
      }

      // Update goal
      Object.keys(updates).forEach(key => {
        if (key !== '_id' && key !== 'createdAt') {
          portfolio.goals[goalIndex][key] = updates[key];
        }
      });

      await portfolio.save();

      res.json({
        success: true,
        message: 'Portfolio goal updated',
        data: portfolio.goals[goalIndex]
      });
    } catch (error) {
      console.error('Update portfolio goal error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to update portfolio goal'
      });
    }
  }

  // Get portfolio analytics
  async getPortfolioAnalytics(req, res) {
    try {
      const userId = req.user._id;
      const { period = 'month' } = req.query;

      const portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        return res.status(404).json({
          success: false,
          message: 'Portfolio not found'
        });
      }

      // Get transaction analytics
      const transactionAnalytics = await this.getTransactionAnalytics(userId, period);

      // Get risk metrics
      const riskMetrics = await this.calculateRiskMetrics(userId);

      // Get diversification score
      const diversificationScore = await this.calculateDiversificationScore(userId);

      res.json({
        success: true,
        data: {
          portfolio: {
            value: portfolio.totalValue.current,
            performance: portfolio.performance
          },
          transactionAnalytics,
          riskMetrics,
          diversificationScore,
          period
        }
      });
    } catch (error) {
      console.error('Get portfolio analytics error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch portfolio analytics'
      });
    }
  }

  // Helper methods
  async getTopPerformingAssets(userId) {
    const assets = await Asset.find({ userId, isHidden: false });
    
    return assets
      .map(asset => ({
        symbol: asset.symbol,
        name: asset.name,
        performance: asset.performance?.dailyChange || 0,
        value: asset.usdValue
      }))
      .sort((a, b) => b.performance - a.performance)
      .slice(0, 5);
  }

  async getTransactionAnalytics(userId, period) {
    let startDate = new Date();
    
    switch (period) {
      case 'week':
        startDate.setDate(startDate.getDate() - 7);
        break;
      case 'month':
        startDate.setMonth(startDate.getMonth() - 1);
        break;
      case 'year':
        startDate.setFullYear(startDate.getFullYear() - 1);
        break;
      default:
        startDate.setMonth(startDate.getMonth() - 1);
    }

    const analytics = await Transaction.aggregate([
      {
        $match: {
          userId: mongoose.Types.ObjectId(userId),
          createdAt: { $gte: startDate }
        }
      },
      {
        $group: {
          _id: {
            type: '$type',
            date: { $dateToString: { format: '%Y-%m-%d', date: '$createdAt' } }
          },
          count: { $sum: 1 },
          totalAmount: { $sum: '$amount' },
          totalUsdValue: { $sum: '$usdValue' }
        }
      },
      {
        $group: {
          _id: '$_id.type',
          dailyData: {
            $push: {
              date: '$_id.date',
              count: '$count',
              amount: '$totalAmount',
              usdValue: '$totalUsdValue'
            }
          },
          totalCount: { $sum: '$count' },
          totalAmount: { $sum: '$totalAmount' },
          totalUsdValue: { $sum: '$totalUsdValue' }
        }
      },
      {
        $project: {
          type: '$_id',
          dailyData: 1,
          totalCount: 1,
          totalAmount: 1,
          totalUsdValue: 1,
          averageAmount: { $divide: ['$totalAmount', '$totalCount'] },
          _id: 0
        }
      }
    ]);

    return analytics;
  }

  async calculateRiskMetrics(userId) {
    const assets = await Asset.find({ userId, isHidden: false });
    const totalValue = assets.reduce((sum, asset) => sum + asset.usdValue, 0);

    if (totalValue === 0) {
      return {
        volatility: 0,
        sharpeRatio: 0,
        maxDrawdown: 0,
        valueAtRisk: 0
      };
    }

    // Mock risk calculations (in production, use historical data)
    return {
      volatility: 15.2, // Annualized volatility percentage
      sharpeRatio: 1.8, // Risk-adjusted return
      maxDrawdown: -8.5, // Maximum peak-to-trough decline
      valueAtRisk: 5.2, // 95% VaR in percentage
      beta: 1.2 // Market correlation
    };
  }

  async calculateDiversificationScore(userId) {
    const assets = await Asset.find({ userId, isHidden: false });
    const totalValue = assets.reduce((sum, asset) => sum + asset.usdValue, 0);

    if (totalValue === 0 || assets.length <= 1) {
      return 0;
    }

    // Calculate Herfindahl-Hirschman Index (HHI)
    let hhi = 0;
    assets.forEach(asset => {
      const marketShare = asset.usdValue / totalValue;
      hhi += marketShare * marketShare;
    });

    // Convert HHI to diversification score (0-100)
    const maxHhi = 1; // Single asset
    const minHhi = 1 / assets.length; // Perfectly diversified
    const score = 100 * (1 - (hhi - minHhi) / (maxHhi - minHhi));

    return Math.min(Math.max(score, 0), 100);
  }
}

module.exports = new PortfolioController();