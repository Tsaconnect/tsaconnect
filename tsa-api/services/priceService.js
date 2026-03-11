const axios = require('axios');
const NodeCache = require('node-cache');

class PriceService {
  constructor() {
    this.cache = new NodeCache({ stdTTL: 60 }); // 60 seconds cache
    this.coinIds = {
      'MCGP': 'maticgold-pro',
      'USDT': 'tether',
      'USDC': 'usd-coin',
      'ETH': 'ethereum',
      'BTC': 'bitcoin',
      'SOL': 'solana',
      'MATIC': 'matic-network',
      'BNB': 'binancecoin',
      'PAXG': 'pax-gold',
      'XAUT': 'tether-gold',
      'GOLD': 'gold',
      'DAI': 'dai',
      'BUSD': 'binance-usd',
      'AVAX': 'avalanche-2',
      'ADA': 'cardano',
      'XRP': 'ripple'
    };
  }

  async getPrices(symbols) {
    try {
      // Check cache first
      const cacheKey = `prices_${symbols.sort().join('_')}`;
      const cached = this.cache.get(cacheKey);
      if (cached) {
        return cached;
      }

      // Get coin IDs
      const coinIds = symbols
        .map(symbol => this.coinIds[symbol.toUpperCase()])
        .filter(id => id);

      if (coinIds.length === 0) {
        return {};
      }

      // Fetch from CoinGecko
      const response = await axios.get(
        'https://api.coingecko.com/api/v3/simple/price',
        {
          params: {
            ids: coinIds.join(','),
            vs_currencies: 'usd',
            include_24hr_change: true,
            include_market_cap: true
          },
          timeout: 5000
        }
      );

      // Map results back to symbols
      const prices = {};
      symbols.forEach(symbol => {
        const coinId = this.coinIds[symbol.toUpperCase()];
        if (coinId && response.data[coinId]) {
          prices[symbol.toUpperCase()] = {
            usd: response.data[coinId].usd,
            usd_24h_change: response.data[coinId].usd_24h_change,
            usd_market_cap: response.data[coinId].usd_market_cap,
            last_updated_at: response.data[coinId].last_updated_at
          };
        }
      });

      // Cache the results
      this.cache.set(cacheKey, prices);

      return prices;
    } catch (error) {
      console.error('Error fetching prices:', error.message);
      
      // Fallback to cached values if available
      const fallbackPrices = {};
      symbols.forEach(symbol => {
        const singleCache = this.cache.get(`price_${symbol.toUpperCase()}`);
        if (singleCache) {
          fallbackPrices[symbol.toUpperCase()] = singleCache;
        }
      });
      
      return fallbackPrices;
    }
  }

  async getHistoricalPrice(symbol, days = 30) {
    try {
      const coinId = this.coinIds[symbol.toUpperCase()];
      if (!coinId) {
        return null;
      }

      const cacheKey = `history_${symbol}_${days}`;
      const cached = this.cache.get(cacheKey);
      if (cached) {
        return cached;
      }

      const response = await axios.get(
        `https://api.coingecko.com/api/v3/coins/${coinId}/market_chart`,
        {
          params: {
            vs_currency: 'usd',
            days: days
          }
        }
      );

      const history = response.data.prices.map(([timestamp, price]) => ({
        timestamp,
        price
      }));

      this.cache.set(cacheKey, history);
      return history;
    } catch (error) {
      console.error(`Error fetching historical price for ${symbol}:`, error.message);
      return null;
    }
  }

  async getMarketData(symbol) {
    try {
      const coinId = this.coinIds[symbol.toUpperCase()];
      if (!coinId) {
        return null;
      }

      const cacheKey = `market_${symbol}`;
      const cached = this.cache.get(cacheKey);
      if (cached) {
        return cached;
      }

      const response = await axios.get(
        `https://api.coingecko.com/api/v3/coins/${coinId}`,
        {
          params: {
            localization: false,
            tickers: false,
            market_data: true,
            community_data: false,
            developer_data: false
          }
        }
      );

      const data = response.data;
      const marketData = {
        symbol: symbol.toUpperCase(),
        name: data.name,
        current_price: data.market_data.current_price.usd,
        price_change_24h: data.market_data.price_change_24h,
        price_change_percentage_24h: data.market_data.price_change_percentage_24h,
        market_cap: data.market_data.market_cap.usd,
        total_volume: data.market_data.total_volume.usd,
        high_24h: data.market_data.high_24h.usd,
        low_24h: data.market_data.low_24h.usd,
        circulating_supply: data.market_data.circulating_supply,
        total_supply: data.market_data.total_supply,
        max_supply: data.market_data.max_supply,
        ath: data.market_data.ath.usd,
        ath_change_percentage: data.market_data.ath_change_percentage.usd,
        ath_date: data.market_data.ath_date.usd,
        atl: data.market_data.atl.usd,
        atl_change_percentage: data.market_data.atl_change_percentage.usd,
        atl_date: data.market_data.atl_date.usd,
        last_updated: data.market_data.last_updated
      };

      this.cache.set(cacheKey, marketData);
      return marketData;
    } catch (error) {
      console.error(`Error fetching market data for ${symbol}:`, error.message);
      return null;
    }
  }
}

module.exports = new PriceService();