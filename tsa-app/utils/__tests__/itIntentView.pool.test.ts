import { deliveryExplorerUrl } from '../itIntentView';
import { txExplorerUrl } from '@/constants/chains';

const HASH = `0x${'cd'.repeat(32)}`;

it('links a settled pool delivery on its chain, as the company wallet does', () => {
  expect(deliveryExplorerUrl({ provider: 'tsa_pool', status: 'settled', txHash: HASH, destChain: 'BSC' }))
    .toBe(txExplorerUrl(HASH, { chainKey: 'bsc' as never }));
  expect(deliveryExplorerUrl({ provider: 'tsa_pool', status: 'pending_payment', txHash: HASH, destChain: 'BSC' })).toBeUndefined();
});
