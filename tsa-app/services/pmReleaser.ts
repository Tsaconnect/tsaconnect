// services/pmReleaser.ts — a payment merchant lets the platform release its trades.
//
// P2PTradeEscrow has no global release power: the platform's release key settles a PM's
// sell-ad trades (after the naira lands) only once the PM's maker wallet has named it with
// setReleaser — one on-chain tx, once per chain. Until then the API refuses to activate the
// PM's sell-ads (409 pm_releaser_not_set).
import { pmReleaserApi } from './p2p';
import { runP2PTx, P2P_TX_COPY } from './p2pTx';
import { P2PCopyError, p2pErrorMessage } from './p2pErrors';

export const PM_RELEASER_NOT_SET = 'pm_releaser_not_set';

/** Makes sure `walletAddress` (the PM's maker wallet) has named the release key on the ad's
 *  chain, sending the setReleaser tx when it has not. Throws a P2PCopyError when it could not. */
export async function ensurePMReleaser(p: {
  walletAddress: string;
  chainId?: number;
  onStage?: (label: string) => void;
  onGasAlert?: (title: string, message: string) => void;
}): Promise<void> {
  p.onStage?.('Checking settlement permission…');
  const status = await pmReleaserApi.status(p.chainId);
  if (!status.success || !status.data) {
    throw new P2PCopyError(p2pErrorMessage(status, 'Could not check your settlement permission. Try again.'));
  }
  const { chainId, wallet, set } = status.data;
  if (set) return;
  if (wallet.toLowerCase() !== p.walletAddress.toLowerCase()) {
    throw new P2PCopyError('Publish from your registered merchant wallet to let TSA settle your trades.');
  }
  p.onStage?.('Allowing TSA to settle your trades…');
  const res = await runP2PTx({
    action: 'set_releaser',
    chainId,
    walletAddress: p.walletAddress,
    prepare: () => pmReleaserApi.prepare(chainId),
    confirm: (txHash) => pmReleaserApi.confirm(txHash, chainId),
    onStage: p.onStage,
    onGasAlert: p.onGasAlert,
  });
  switch (res.status) {
    case 'confirmed':
      return;
    case 'refused':
      throw new P2PCopyError(p2pErrorMessage(res.response, 'Could not prepare the settlement permission. Try again.'));
    case 'reverted':
      throw new P2PCopyError(`${P2P_TX_COPY.failedTitle('Settlement permission')}. Nothing changed — try again.`);
    case 'confirm-failed':
      throw new P2PCopyError(`${P2P_TX_COPY.stillConfirmingTitle('Settlement permission')}. Publish again in a minute.`);
    case 'busy':
      throw new P2PCopyError('Already allowing settlement — wait a moment.');
    default:
      throw new P2PCopyError('Add a little gas to your wallet, then publish again.');
  }
}
