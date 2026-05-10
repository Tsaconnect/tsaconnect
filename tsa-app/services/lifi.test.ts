import { getLiFiTokens, getLiFiQuote, buildERC20ApproveCalldata } from './lifi';

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue('Bearer test-token'),
}));

const mockFetch = jest.fn();
global.fetch = mockFetch;

const MOCK_QUOTE = {
  toAmount: '1000000000000000000',
  toAmountUSD: '1000',
  tool: '1inch',
  transactionRequest: {
    to: '0xrouter',
    data: '0xdeadbeef',
    value: '0x0',
    chainId: 56,
    gasLimit: '200000',
    gasPrice: '5000000000',
  },
};

beforeEach(() => {
  mockFetch.mockReset();
});

describe('getLiFiTokens', () => {
  it('returns token list on success', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ tokens: { '56': [{ address: '0xabc', symbol: 'USDT', name: 'Tether', decimals: 6, logoURI: '' }] } }),
    });
    const tokens = await getLiFiTokens(56);
    expect(tokens).toHaveLength(1);
    expect(tokens[0].symbol).toBe('USDT');
    expect(mockFetch).toHaveBeenCalledWith(expect.stringContaining('/swap/lifi/tokens?chainId=56'), expect.any(Object));
  });

  it('throws on non-ok response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, json: async () => ({ message: 'error' }) });
    await expect(getLiFiTokens(56)).rejects.toThrow();
  });

  it('returns empty array when chain tokens missing from response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => ({ tokens: {} }) });
    const tokens = await getLiFiTokens(56);
    expect(tokens).toEqual([]);
  });
});

describe('getLiFiQuote', () => {
  it('returns quote with transactionRequest on success', async () => {
    mockFetch.mockResolvedValueOnce({ ok: true, json: async () => MOCK_QUOTE });
    const quote = await getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' });
    expect(quote.transactionRequest.to).toBe('0xrouter');
    expect(quote.approvalAddress).toBeUndefined();
  });

  it('exposes approvalAddress and approvalToken when present', async () => {
    mockFetch.mockResolvedValueOnce({
      ok: true,
      json: async () => ({ ...MOCK_QUOTE, estimate: { approvalAddress: '0xspender' }, action: { fromToken: { address: '0xtoken' } } }),
    });
    const quote = await getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' });
    expect(quote.approvalAddress).toBe('0xspender');
    expect(quote.approvalToken).toBe('0xtoken');
  });

  it('throws on non-ok response', async () => {
    mockFetch.mockResolvedValueOnce({ ok: false, json: async () => ({ message: 'No route found' }) });
    await expect(getLiFiQuote({ fromChain: 56, toChain: 1, fromToken: '0xabc', toToken: '0xdef', fromAmount: '1000000', fromAddress: '0x123' })).rejects.toThrow('No route found');
  });
});

describe('buildERC20ApproveCalldata', () => {
  it('starts with approve selector 0x095ea7b3', () => {
    const data = buildERC20ApproveCalldata('0x1234567890123456789012345678901234567890', '1000000');
    expect(data.startsWith('0x095ea7b3')).toBe(true);
  });

  it('encodes spender address in calldata', () => {
    const spender = '0x1234567890123456789012345678901234567890';
    const data = buildERC20ApproveCalldata(spender, '1000000');
    expect(data.toLowerCase()).toContain('1234567890123456789012345678901234567890');
  });
});
