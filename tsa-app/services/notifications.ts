// Device-token registration for Expo push. Thin authed fetch (mirrors the auth
// pattern used elsewhere — the stored token is already "Bearer <jwt>").
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

async function authHeader(): Promise<Record<string, string>> {
  const token = await AsyncStorage.getItem('token');
  if (!token) return {};
  return { Authorization: token.startsWith('Bearer') ? token : `Bearer ${token}` };
}

/** Register (or re-point, server-side upsert) this device's Expo push token. */
export async function registerDevice(token: string, platform: string): Promise<boolean> {
  try {
    const res = await fetch(`${API_BASE_URL}/notifications/device-tokens`, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...(await authHeader()) },
      body: JSON.stringify({ token, platform }),
    });
    return res.ok;
  } catch {
    return false;
  }
}

/** Remove a device's push token (best-effort; server also prunes dead tokens). */
export async function unregisterDevice(token: string): Promise<boolean> {
  try {
    const res = await fetch(`${API_BASE_URL}/notifications/device-tokens/${encodeURIComponent(token)}`, {
      method: 'DELETE',
      headers: { ...(await authHeader()) },
    });
    return res.ok;
  } catch {
    return false;
  }
}
