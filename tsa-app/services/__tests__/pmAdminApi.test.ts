// services/__tests__/pmAdminApi.test.ts
//
// Unit tests for the new pmAdminApi methods:
//   listApplications    — GET /admin/merchant-requests?status=pending (filtered to p2p_merchant)
//   approveApplication  — POST /admin/merchant-requests/:id/approve
//   listApprovedApplications — GET /admin/merchant-requests?status=approved (filtered to p2p_merchant)
//
// Mocks: fetch (global) + walletApi.getAuthHeaders (AsyncStorage chain)
// Strategy: test only the client-side filtering behaviour and request shape;
// network semantics are covered by the shared `request` helper which is
// exercised exhaustively in the other service tests.

jest.mock('@react-native-async-storage/async-storage', () => ({
  getItem: jest.fn().mockResolvedValue(null),
}));

jest.mock('../walletApi', () => ({
  getAuthHeaders: jest.fn().mockResolvedValue({ 'Content-Type': 'application/json' }),
}));

// eslint-disable-next-line import/first
import { pmAdminApi, type MerchantRequest } from '../p2p';

// Helper: build a minimal MerchantRequest fixture
function mr(over: Partial<MerchantRequest> = {}): MerchantRequest {
  return {
    id: 'req-1',
    userId: 'user-1',
    businessType: 'p2p_merchant',
    businessName: 'Acme Corp',
    status: 'pending',
    createdAt: '2026-01-01T00:00:00Z',
    updatedAt: '2026-01-01T00:00:00Z',
    ...over,
  };
}

// Build a fetch mock that returns the given body with the given HTTP status.
function mockFetch(body: unknown, status = 200) {
  return jest.fn().mockResolvedValue({
    status,
    text: () => Promise.resolve(JSON.stringify(body)),
  });
}

afterEach(() => {
  jest.restoreAllMocks();
});

// ─── listApplications ────────────────────────────────────────────────────────

describe('pmAdminApi.listApplications', () => {
  it('keeps only p2p_merchant rows from a mixed backend response', async () => {
    const payload = {
      success: true,
      data: {
        requests: [
          mr({ id: 'pm-1', businessType: 'p2p_merchant' }),
          mr({ id: 'gp-1', businessType: 'general_products' }),
          mr({ id: 'pm-2', businessType: 'p2p_merchant' }),
        ],
        total: 3,
        page: 1,
        limit: 100,
      },
    };
    global.fetch = mockFetch(payload);
    const res = await pmAdminApi.listApplications();
    expect(res.success).toBe(true);
    expect(res.data?.requests).toHaveLength(2);
    expect(res.data?.requests.every((r) => r.businessType === 'p2p_merchant')).toBe(true);
  });

  it('returns an empty array when there are no p2p_merchant rows', async () => {
    const payload = {
      success: true,
      data: {
        requests: [mr({ id: 'gp-1', businessType: 'general_products' })],
        total: 1,
        page: 1,
        limit: 100,
      },
    };
    global.fetch = mockFetch(payload);
    const res = await pmAdminApi.listApplications();
    expect(res.success).toBe(true);
    expect(res.data?.requests).toHaveLength(0);
  });

  it('passes through a backend error response unchanged', async () => {
    const payload = { success: false, message: 'Unauthorized' };
    global.fetch = mockFetch(payload, 401);
    const res = await pmAdminApi.listApplications();
    expect(res.success).toBe(false);
    expect(res.message).toBe('Unauthorized');
  });

  it('sends status=pending and limit=100 query params', async () => {
    const payload = {
      success: true,
      data: { requests: [], total: 0, page: 1, limit: 100 },
    };
    global.fetch = mockFetch(payload);
    await pmAdminApi.listApplications();
    const calledUrl = (global.fetch as jest.Mock).mock.calls[0][0] as string;
    expect(calledUrl).toContain('status=pending');
    expect(calledUrl).toContain('limit=100');
  });
});

// ─── listApprovedApplications ─────────────────────────────────────────────────

describe('pmAdminApi.listApprovedApplications', () => {
  it('keeps only p2p_merchant rows with approved status', async () => {
    const payload = {
      success: true,
      data: {
        requests: [
          mr({ id: 'a-1', status: 'approved', businessType: 'p2p_merchant' }),
          mr({ id: 'a-2', status: 'approved', businessType: 'general_products' }),
        ],
        total: 2,
        page: 1,
        limit: 100,
      },
    };
    global.fetch = mockFetch(payload);
    const res = await pmAdminApi.listApprovedApplications();
    expect(res.success).toBe(true);
    expect(res.data?.requests).toHaveLength(1);
    expect(res.data?.requests[0].id).toBe('a-1');
  });

  it('sends status=approved query param', async () => {
    const payload = {
      success: true,
      data: { requests: [], total: 0, page: 1, limit: 100 },
    };
    global.fetch = mockFetch(payload);
    await pmAdminApi.listApprovedApplications();
    const calledUrl = (global.fetch as jest.Mock).mock.calls[0][0] as string;
    expect(calledUrl).toContain('status=approved');
  });
});

// ─── approveApplication ───────────────────────────────────────────────────────

describe('pmAdminApi.approveApplication', () => {
  it('posts to the correct endpoint', async () => {
    const payload = { success: true, data: mr({ id: 'req-abc', status: 'approved' }) };
    global.fetch = mockFetch(payload);
    const res = await pmAdminApi.approveApplication('req-abc');
    expect(res.success).toBe(true);
    const calledUrl = (global.fetch as jest.Mock).mock.calls[0][0] as string;
    expect(calledUrl).toContain('/admin/merchant-requests/req-abc/approve');
  });

  it('uses POST method', async () => {
    global.fetch = mockFetch({ success: true, data: mr() });
    await pmAdminApi.approveApplication('req-xyz');
    const calledInit = (global.fetch as jest.Mock).mock.calls[0][1] as RequestInit;
    expect(calledInit.method).toBe('POST');
  });

  it('surfaces a backend error message on 5xx', async () => {
    const payload = { success: false, message: 'Nomba provisioning failed — retry' };
    global.fetch = mockFetch(payload, 503);
    const res = await pmAdminApi.approveApplication('req-fail');
    expect(res.success).toBe(false);
    expect(res.message).toContain('Nomba provisioning failed');
  });
});
