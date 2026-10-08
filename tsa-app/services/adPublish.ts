// services/adPublish.ts — create → sign → activate a sell ad, extracted from
// create-ad.tsx (its steps 4–6) so the TSA Liquidity stock ad publishes through
// the same path. Funding is the caller's job and must already be done: this
// never moves funds, it only creates the ad and signs its voucher (no gas).
import { adsApi, type CreateAdInput, type P2PAd } from './p2p';
import { signTypedData } from './wallet';

export async function createAndSignAd(p: {
  payload: CreateAdInput;
  sellerAddress: string;
  onStage?: (s: string) => void;
  statusError: (status?: number, message?: string) => string;
}): Promise<P2PAd> {
  // The server creates the ad `pending` and returns the EIP-712 voucher.
  p.onStage?.('Building voucher…');
  const createRes = await adsApi.create(p.payload);
  if (!createRes.success || !createRes.data) throw new Error(p.statusError(createRes.status, createRes.message));
  const { ad, typedData } = createRes.data;

  // Pinned to sellerAddress so the signature recovers to the wallet the ad
  // declares — a stale active wallet would otherwise fail with the cryptic
  // "recovered != expected" error.
  p.onStage?.('Signing voucher…');
  const signature = await signTypedData(typedData.domain, typedData.types, typedData.message, p.sellerAddress);

  p.onStage?.('Activating ad…');
  const signRes = await adsApi.sign(ad.id, signature, p.sellerAddress);
  if (!signRes.success || !signRes.data) throw new Error(p.statusError(signRes.status, signRes.message));
  // A 2xx is not proof the ad went live (a server regression could leave it
  // pending); confirm before the caller says it did.
  if (signRes.data.status !== 'active') {
    throw new Error(
      `Server returned ad status "${signRes.data.status}" — refresh My Ads and contact support if it stays this way.`,
    );
  }
  return signRes.data;
}
