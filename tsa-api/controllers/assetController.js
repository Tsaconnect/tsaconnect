const Asset = require('../models/Asset');
const Wallet = require('../models/Wallet');
const Portfolio = require('../models/Portfolio');
const priceService = require('../services/priceService');

class AssetController {
  // Get user assets with real-time prices
  async getUserAssets(req, res) {
    try {
      const userId = req.user._id;
      const { showHidden = false } = req.query;

      // Build query
      const query = { userId };
      if (!showHidden) {
        query.isHidden = false;
      }

      // Get assets
      const assets = await Asset.find(query)
        .sort({ usdValue: -1 })
        .lean();

      // Extract symbols for price fetching
      const symbols = [...new Set(assets.map(asset => asset.symbol))];
      
      // Fetch current prices
      let prices = {};
      if (symbols.length > 0) {
        prices = await priceService.getPrices(symbols);
      }

      // Update assets with current prices
      const updatedAssets = assets.map(asset => {
        const priceData = prices[asset.symbol] || {};
        const currentPrice = priceData.usd || (asset.usdValue / asset.balance) || 0;
        const currentUsdValue = asset.balance * currentPrice;
        const priceChange = priceData.usd_24h_change || 0;

        return {
          ...asset,
          currentPrice,
          usdValue: currentUsdValue,
          priceChange24h: priceChange,
          formattedBalance: parseFloat(asset.balance.toFixed(6))
        };
      });

      // Calculate totals
      const totals = {
        balance: updatedAssets.reduce((sum, asset) => sum + asset.balance, 0),
        usdValue: updatedAssets.reduce((sum, asset) => sum + asset.usdValue, 0),
        dailyChange: updatedAssets.reduce((sum, asset) => {
          const assetChange = (asset.usdValue * (asset.priceChange24h || 0)) / 100;
          return sum + assetChange;
        }, 0)
      };

      // Get selected asset
      const selectedAsset = await Asset.findOne({ userId, isSelected: true });

      res.json({
        success: true,
        data: {
          assets: updatedAssets,
          totals,
          selectedAsset,
          lastUpdated: new Date()
        }
      });
    } catch (error) {
      console.error('Get user assets error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch assets'
      });
    }
  }

  // Get asset details
  async getAssetDetails(req, res) {
    try {
      const { assetId } = req.params;
      const userId = req.user._id;

      const asset = await Asset.findOne({
        _id: assetId,
        userId
      });

      if (!asset) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      // Get market data for this asset
      const marketData = await priceService.getMarketData(asset.symbol);
      const historicalData = await priceService.getHistoricalPrice(asset.symbol, 30);

      res.json({
        success: true,
        data: {
          asset,
          marketData,
          historicalData: historicalData || []
        }
      });
    } catch (error) {
      console.error('Get asset details error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch asset details'
      });
    }
  }

  // Select asset for debit account
  async selectAsset(req, res) {
    try {
      const { assetId } = req.body;
      const userId = req.user._id;

      // Verify asset exists and belongs to user
      const asset = await Asset.findOne({
        _id: assetId,
        userId
      });

      if (!asset) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      // Unselect all other assets
      await Asset.updateMany(
        { userId, _id: { $ne: assetId } },
        { $set: { isSelected: false } }
      );

      // Select the new asset
      asset.isSelected = true;
      await asset.save();

      // Update wallet selected asset
      await Wallet.findOneAndUpdate(
        { userId },
        { $set: { selectedAsset: assetId } }
      );

      res.json({
        success: true,
        message: `${asset.symbol} selected as debit account`,
        data: asset
      });
    } catch (error) {
      console.error('Select asset error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to select asset'
      });
    }
  }

  // Toggle asset visibility
  async toggleAssetVisibility(req, res) {
    try {
      const { assetId } = req.params;
      const userId = req.user._id;

      const asset = await Asset.findOne({
        _id: assetId,
        userId
      });

      if (!asset) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      asset.isHidden = !asset.isHidden;
      await asset.save();

      res.json({
        success: true,
        message: `Asset ${asset.isHidden ? 'hidden' : 'visible'}`,
        data: asset
      });
    } catch (error) {
      console.error('Toggle asset visibility error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to update asset visibility'
      });
    }
  }

  // Refresh asset prices
  async refreshAssetPrices(req, res) {
    try {
      const userId = req.user._id;
      const { assetId } = req.query;

      let assets;
      if (assetId) {
        assets = [await Asset.findOne({ _id: assetId, userId })];
      } else {
        assets = await Asset.find({ userId, isHidden: false });
      }

      if (!assets || assets.length === 0) {
        return res.status(404).json({
          success: false,
          message: 'No assets found'
        });
      }

      // Extract symbols for price fetching
      const symbols = [...new Set(assets.map(asset => asset.symbol))];
      const prices = await priceService.getPrices(symbols);

      // Update each asset
      const updatePromises = assets.map(async (asset) => {
        if (prices[asset.symbol]) {
          const price = prices[asset.symbol].usd;
          if (price) {
            await asset.updateValue(price);
          }
        }
        return asset;
      });

      await Promise.all(updatePromises);

      // Update portfolio value
      await this.updatePortfolioValue(userId);

      res.json({
        success: true,
        message: 'Asset prices refreshed successfully',
        refreshedAt: new Date()
      });
    } catch (error) {
      console.error('Refresh asset prices error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to refresh asset prices'
      });
    }
  }

  // Add new asset
  async addAsset(req, res) {
    try {
      const userId = req.user._id;
      const { symbol, name, balance, details } = req.body;

      // Check if asset already exists
      const existingAsset = await Asset.findOne({ userId, symbol: symbol.toUpperCase() });
      if (existingAsset) {
        return res.status(400).json({
          success: false,
          message: 'Asset already exists'
        });
      }

      // Get current price
      const prices = await priceService.getPrices([symbol]);
      const currentPrice = prices[symbol]?.usd || 0;
      const usdValue = balance * currentPrice;

      // Create new asset
      const asset = await Asset.create({
        userId,
        symbol: symbol.toUpperCase(),
        name,
        balance,
        usdValue,
        details: details || {
          type: 'token',
          chain: 'ethereum'
        }
      });

      // Update portfolio
      await this.updatePortfolioValue(userId);

      res.status(201).json({
        success: true,
        message: 'Asset added successfully',
        data: asset
      });
    } catch (error) {
      console.error('Add asset error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to add asset'
      });
    }
  }

  // Update asset balance
  async updateAssetBalance(req, res) {
    try {
      const { assetId } = req.params;
      const { balance } = req.body;
      const userId = req.user._id;

      if (balance < 0) {
        return res.status(400).json({
          success: false,
          message: 'Balance cannot be negative'
        });
      }

      const asset = await Asset.findOne({ _id: assetId, userId });
      if (!asset) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      // Get current price
      const prices = await priceService.getPrices([asset.symbol]);
      const currentPrice = prices[asset.symbol]?.usd || (asset.usdValue / asset.balance) || 0;

      asset.balance = balance;
      asset.usdValue = balance * currentPrice;
      await asset.save();

      // Update portfolio
      await this.updatePortfolioValue(userId);

      res.json({
        success: true,
        message: 'Asset balance updated',
        data: asset
      });
    } catch (error) {
      console.error('Update asset balance error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to update asset balance'
      });
    }
  }

  // Get asset performance
  async getAssetPerformance(req, res) {
    try {
      const { assetId } = req.params;
      const userId = req.user._id;

      const asset = await Asset.findOne({ _id: assetId, userId });
      if (!asset) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      // Get historical data
      const historicalData = await priceService.getHistoricalPrice(asset.symbol, 90);
      
      // Calculate performance metrics
      const performance = {
        daily: 0,
        weekly: 0,
        monthly: 0,
        allTime: 0
      };

      if (historicalData && historicalData.length > 0) {
        const currentPrice = historicalData[historicalData.length - 1].price;
        const dayAgoPrice = historicalData[Math.max(0, historicalData.length - 2)]?.price || currentPrice;
        const weekAgoPrice = historicalData[Math.max(0, historicalData.length - 8)]?.price || currentPrice;
        const monthAgoPrice = historicalData[Math.max(0, historicalData.length - 31)]?.price || currentPrice;
        const allTimePrice = historicalData[0]?.price || currentPrice;

        performance.daily = ((currentPrice - dayAgoPrice) / dayAgoPrice) * 100;
        performance.weekly = ((currentPrice - weekAgoPrice) / weekAgoPrice) * 100;
        performance.monthly = ((currentPrice - monthAgoPrice) / monthAgoPrice) * 100;
        performance.allTime = ((currentPrice - allTimePrice) / allTimePrice) * 100;
      }

      res.json({
        success: true,
        data: {
          asset,
          performance,
          historicalData: historicalData || []
        }
      });
    } catch (error) {
      console.error('Get asset performance error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch asset performance'
      });
    }
  }

  // Helper method to update portfolio value
  async updatePortfolioValue(userId) {
    try {
      const assets = await Asset.find({ userId, isHidden: false });
      const totalValue = assets.reduce((sum, asset) => sum + asset.usdValue, 0);

      let portfolio = await Portfolio.findOne({ userId });
      if (!portfolio) {
        portfolio = await Portfolio.create({ userId });
      }

      await portfolio.updateValue(totalValue);

      // Update wallet total value
      await Wallet.findOneAndUpdate(
        { userId },
        { $set: { totalUSDValue: totalValue } }
      );
    } catch (error) {
      console.error('Update portfolio value error:', error);
    }
  }
}

module.exports = new AssetController();