const priceService = require('../services/priceService');

class MarketController {
  // Get market overview
  async getMarketOverview(req, res) {
    try {
      const { symbols = ['BTC', 'ETH', 'MCGP', 'USDT', 'USDC'] } = req.query;
      const symbolArray = Array.isArray(symbols) ? symbols : symbols.split(',');

      // Get prices for requested symbols
      const prices = await priceService.getPrices(symbolArray);
      
      // Get market indices
      const marketIndices = await this.getMarketIndices();
      
      // Get trending assets
      const trendingAssets = await this.getTrendingAssets();

      res.json({
        success: true,
        data: {
          prices,
          marketIndices,
          trendingAssets,
          updatedAt: new Date()
        }
      });
    } catch (error) {
      console.error('Get market overview error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch market overview'
      });
    }
  }

  // Get asset price history
  async getAssetPriceHistory(req, res) {
    try {
      const { symbol } = req.params;
      const { days = 30, interval = 'daily' } = req.query;

      if (!symbol) {
        return res.status(400).json({
          success: false,
          message: 'Symbol is required'
        });
      }

      const history = await priceService.getHistoricalPrice(symbol, parseInt(days));
      
      // Process data based on interval
      let processedHistory = history || [];
      if (interval === 'hourly' && history) {
        // Sample hourly data (in production, fetch hourly data)
        processedHistory = this.sampleData(history, 24);
      } else if (interval === 'weekly' && history) {
        processedHistory = this.sampleData(history, 7);
      }

      res.json({
        success: true,
        data: {
          symbol,
          history: processedHistory,
          interval,
          days: parseInt(days)
        }
      });
    } catch (error) {
      console.error('Get asset price history error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch price history'
      });
    }
  }

  // Get asset details
  async getAssetDetails(req, res) {
    try {
      const { symbol } = req.params;

      if (!symbol) {
        return res.status(400).json({
          success: false,
          message: 'Symbol is required'
        });
      }

      const marketData = await priceService.getMarketData(symbol);
      
      if (!marketData) {
        return res.status(404).json({
          success: false,
          message: 'Asset not found'
        });
      }

      // Get related news (mock)
      const news = await this.getAssetNews(symbol);

      // Get similar assets
      const similarAssets = await this.getSimilarAssets(symbol);

      res.json({
        success: true,
        data: {
          ...marketData,
          news,
          similarAssets
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

  // Search assets
  async searchAssets(req, res) {
    try {
      const { query } = req.params;
      
      if (!query || query.length < 2) {
        return res.status(400).json({
          success: false,
          message: 'Search query must be at least 2 characters'
        });
      }

      // Mock search results (in production, use database or API)
      const searchResults = this.mockSearchAssets(query);

      res.json({
        success: true,
        data: searchResults
      });
    } catch (error) {
      console.error('Search assets error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to search assets'
      });
    }
  }

  // Get watchlist
  async getWatchlist(req, res) {
    try {
      const userId = req.user._id;
      
      // Get user's assets for watchlist (in production, would have separate watchlist model)
      const assets = await Asset.find({ userId, isHidden: false })
        .select('symbol name')
        .limit(20);

      const symbols = assets.map(asset => asset.symbol);
      const prices = await priceService.getPrices(symbols);

      const watchlist = assets.map(asset => ({
        ...asset.toObject(),
        price: prices[asset.symbol]?.usd || 0,
        change24h: prices[asset.symbol]?.usd_24h_change || 0
      }));

      res.json({
        success: true,
        data: watchlist
      });
    } catch (error) {
      console.error('Get watchlist error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to fetch watchlist'
      });
    }
  }

  // Add to watchlist
  async addToWatchlist(req, res) {
    try {
      const { symbol } = req.body;
      
      if (!symbol) {
        return res.status(400).json({
          success: false,
          message: 'Symbol is required'
        });
      }

      // In production, add to Watchlist model
      // For now, return success
      res.json({
        success: true,
        message: 'Added to watchlist'
      });
    } catch (error) {
      console.error('Add to watchlist error:', error);
      res.status(500).json({
        success: false,
        message: 'Failed to add to watchlist'
      });
    }
  }

  // Get market indices
  async getMarketIndices() {
    // Mock market indices (in production, fetch from API)
    return {
      cryptoMarketCap: {
        value: 1600000000000,
        change24h: 2.5
      },
      bitcoinDominance: {
        value: 52.8,
        change24h: 0.3
      },
      fearAndGreed: {
        value: 65,
        level: 'Greed',
        change24h: 5
      },
      totalVolume: {
        value: 65000000000,
        change24h: 8.2
      }
    };
  }

  // Get trending assets
  async getTrendingAssets() {
    // Mock trending assets (in production, fetch from API)
    return [
      {
        symbol: 'MCGP',
        name: 'MaticGold Pro',
        price: 1.02,
        change24h: 3.5,
        volume: 2500000
      },
      {
        symbol: 'ETH',
        name: 'Ethereum',
        price: 2200,
        change24h: 2.8,
        volume: 12000000000
      },
      {
        symbol: 'SOL',
        name: 'Solana',
        price: 95.50,
        change24h: 5.2,
        volume: 2500000000
      },
      {
        symbol: 'MATIC',
        name: 'Polygon',
        price: 0.85,
        change24h: 1.8,
        volume: 450000000
      }
    ];
  }

  // Get asset news
  async getAssetNews(symbol) {
    // Mock news (in production, fetch from news API)
    return [
      {
        title: `${symbol} Shows Strong Performance This Week`,
        source: 'CryptoNews',
        publishedAt: new Date(Date.now() - 3600000),
        url: '#',
        summary: `${symbol} has shown remarkable growth over the past week...`
      },
      {
        title: `New Partnership Announced for ${symbol}`,
        source: 'BlockchainDaily',
        publishedAt: new Date(Date.now() - 7200000),
        url: '#',
        summary: `The team behind ${symbol} has announced a major partnership...`
      }
    ];
  }

  // Get similar assets
  async getSimilarAssets(symbol) {
    // Mock similar assets based on symbol
    const similarMap = {
      'MCGP': ['PAXG', 'XAUT', 'GOLD'],
      'USDT': ['USDC', 'DAI', 'BUSD'],
      'ETH': ['SOL', 'AVAX', 'ADA'],
      'BTC': ['ETH', 'BNB', 'XRP']
    };

    const similarSymbols = similarMap[symbol] || ['ETH', 'BTC', 'SOL'];
    const prices = await priceService.getPrices(similarSymbols);

    return similarSymbols.map(sym => ({
      symbol: sym,
      name: this.getAssetName(sym),
      price: prices[sym]?.usd || 0,
      change24h: prices[sym]?.usd_24h_change || 0
    }));
  }

  // Mock search
  mockSearchAssets(query) {
    const allAssets = [
      { symbol: 'MCGP', name: 'MaticGold Pro', type: 'gold-backed' },
      { symbol: 'USDT', name: 'Tether', type: 'stablecoin' },
      { symbol: 'USDC', name: 'USD Coin', type: 'stablecoin' },
      { symbol: 'ETH', name: 'Ethereum', type: 'crypto' },
      { symbol: 'BTC', name: 'Bitcoin', type: 'crypto' },
      { symbol: 'SOL', name: 'Solana', type: 'crypto' },
      { symbol: 'MATIC', name: 'Polygon', type: 'crypto' },
      { symbol: 'BNB', name: 'Binance Coin', type: 'crypto' }
    ];

    const queryLower = query.toLowerCase();
    return allAssets.filter(asset =>
      asset.symbol.toLowerCase().includes(queryLower) ||
      asset.name.toLowerCase().includes(queryLower)
    );
  }

  // Get asset name
  getAssetName(symbol) {
    const names = {
      'MCGP': 'MaticGold Pro',
      'USDT': 'Tether',
      'USDC': 'USD Coin',
      'ETH': 'Ethereum',
      'BTC': 'Bitcoin',
      'SOL': 'Solana',
      'MATIC': 'Polygon',
      'BNB': 'Binance Coin',
      'PAXG': 'Pax Gold',
      'XAUT': 'Tether Gold',
      'GOLD': 'Gold Token',
      'DAI': 'Dai',
      'BUSD': 'Binance USD',
      'AVAX': 'Avalanche',
      'ADA': 'Cardano',
      'XRP': 'Ripple'
    };

    return names[symbol] || symbol;
  }

  // Sample data for different intervals
  sampleData(data, targetPoints) {
    if (!data || data.length <= targetPoints) {
      return data;
    }

    const step = Math.floor(data.length / targetPoints);
    const sampled = [];
    
    for (let i = 0; i < data.length; i += step) {
      if (sampled.length >= targetPoints) break;
      sampled.push(data[i]);
    }

    return sampled;
  }
}

module.exports = new MarketController();