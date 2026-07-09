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
  const prepRes = await fetch(`${API_BASE_URL}/wallet/nonevm/prepare-tx?chain=tron`, {
    method: 'POST', headers, body: JSON.stringify(body),
  });
  if (!prepRes.ok) throw new Error(`prepare-tx failed (${prepRes.status})`);
  const prep = await prepRes.json();
  if (!prep?.data?.prepared) throw new Error(prep?.message || 'prepare failed');

  const { prepared } = prep.data;
  const pk = await getPrivateKey(params.walletAddress);
  if (!pk) throw new Error('no key for wallet');
  const payloads: { index: number; hashHex: string }[] = prepared.payloads;
  if (!Array.isArray(payloads) || payloads.length === 0) {
    throw new Error('prepare-tx returned no payloads');
  }
  const sigs = payloads.map((p) => ({ index: p.index, sigHex: signSecpDigest(p.hashHex, pk) }));

  const subRes = await fetch(`${API_BASE_URL}/wallet/nonevm/submit-tx?chain=tron`, {
    method: 'POST', headers,
    body: JSON.stringify({
      prepared, sigs,
      from: params.from, amount: params.amount, symbol: params.symbol,
    }),
  });
  if (!subRes.ok) throw new Error(`submit-tx failed (${subRes.status})`);
  const sub = await subRes.json();
  if (!sub?.data?.txHash) throw new Error(sub?.message || 'broadcast failed');
  return { txHash: sub.data.txHash };
}
