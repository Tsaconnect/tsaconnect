class BlockchainService {
  constructor() {
    console.log('Blockchain service initialized (mock mode)');
  }

  async getTransactionReceipt(txHash, blockchain) {
    // Mock response
    return {
      verified: true,
      receipt: {
        hash: txHash,
        blockNumber: Math.floor(Math.random() * 1000000),
        confirmations: 12,
        status: 'success',
        timestamp: new Date().toISOString()
      }
    };
  }

  async getGasPrices(chain = 'ethereum') {
    return {
      slow: { gasPrice: '20' },
      average: { gasPrice: '25' },
      fast: { gasPrice: '30' }
    };
  }

  async verifyTransaction(txHash, chain = 'ethereum') {
    return {
      verified: true,
      receipt: {
        hash: txHash,
        status: 'success',
        blockNumber: 123456
      }
    };
  }
}

module.exports = new BlockchainService();