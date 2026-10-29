// services/__tests__/pmReleaser.test.ts — a payment merchant naming the release key.
const mockStatus = jest.fn();
const mockPrepare = jest.fn();
const mockConfirm = jest.fn();
jest.mock('../p2p', () => ({
  pmReleaserApi: {
    status: (...a: any[]) => mockStatus(...a),
    prepare: (...a: any[]) => mockPrepare(...a),
    confirm: (...a: any[]) => mockConfirm(...a),
  },
}));
const mockRun = jest.fn();
jest.mock('../p2pTx', () => ({
  runP2PTx: (...a: any[]) => mockRun(...a),
  P2P_TX_COPY: { failedTitle: (l: string) => `${l} failed`, stillConfirmingTitle: (l: string) => `${l} sent — still confirming` },
}));

// eslint-disable-next-line import/first
import { ensurePMReleaser } from '../pmReleaser';

const status = (over: object = {}) => ({
  success: true,
  data: { chainId: 146, wallet: '0xAbC', releaser: '0x0', expected: '0xKey', set: false, ...over },
});

beforeEach(() => jest.resetAllMocks());

it('does nothing when the release key is already named', async () => {
  mockStatus.mockResolvedValue(status({ set: true }));
  await ensurePMReleaser({ walletAddress: '0xabc' });
  expect(mockRun).not.toHaveBeenCalled();
});

it('sends setReleaser on the chain the API reports, as the maker wallet', async () => {
  mockStatus.mockResolvedValue(status());
  mockRun.mockResolvedValue({ status: 'confirmed', txHash: '0xtx' });
  await ensurePMReleaser({ walletAddress: '0xabc', chainId: 146 });
  const params = mockRun.mock.calls[0][0];
  expect(params).toMatchObject({ action: 'set_releaser', chainId: 146, walletAddress: '0xabc' });
  await params.prepare();
  await params.confirm('0xtx');
  expect(mockPrepare).toHaveBeenCalledWith(146);
  expect(mockConfirm).toHaveBeenCalledWith('0xtx', 146);
});

it('refuses a wallet other than the registered maker wallet', async () => {
  mockStatus.mockResolvedValue(status());
  await expect(ensurePMReleaser({ walletAddress: '0xdef' })).rejects.toThrow(/registered merchant wallet/);
  expect(mockRun).not.toHaveBeenCalled();
});

it('turns every unsent or unrecorded outcome into copy', async () => {
  mockStatus.mockResolvedValue(status());
  for (const res of [{ status: 'reverted' }, { status: 'confirm-failed' }, { status: 'aborted' }, { status: 'busy' }]) {
    mockRun.mockResolvedValueOnce(res);
    await expect(ensurePMReleaser({ walletAddress: '0xabc' })).rejects.toThrow();
  }
});
