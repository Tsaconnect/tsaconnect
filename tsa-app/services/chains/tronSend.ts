import { API_BASE_URL } from '../../constants/api/config';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { getPrivateKey } from '../wallet';
import { signSecpDigest } from './sign';

async function authHeaders() {
  const token = await AsyncStorage.getItem('authToken');
  return { 'Content-Type': 'application/json', Authorization: `Bearer ${token}` };
}

export async function sendTron(params: {
  from: string; to: string; amount: string; symbol: string; tokenAddr?: string; walletAddress: string;
}): Promise<{ txHash: string }> {
  const headers = await authHeaders();
  const body = {
    from: params.from, to: params.to, amount: params.amount,
    symbol: params.symbol, tokenAddr: params.tokenAddr ?? '',
  };
  const prep = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=tron`, {
    method: 'POST', headers, body: JSON.stringify(body),
  }).then((r) => r.json());
  if (!prep?.data?.prepared) throw new Error(prep?.message || 'prepare failed');

  const { prepared } = prep.data;
  const pk = await getPrivateKey(params.walletAddress);
  if (!pk) throw new Error('no key for wallet');
  const sigs = prepared.payloads.map((p: { index: number; hashHex: string }) => ({
    index: p.index, sigHex: signSecpDigest(p.hashHex, pk),
  }));

  const sub = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=tron`, {
    method: 'POST', headers, body: JSON.stringify({ prepared, sigs }),
  }).then((r) => r.json());
  if (!sub?.data?.txHash) throw new Error(sub?.message || 'broadcast failed');
  return { txHash: sub.data.txHash };
}
