// Uploads a local image to the platform CDN (multipart POST /upload) and
// returns its URL. The one upload path for chat images and proof/evidence
// tiles; the API only accepts chat and proof images hosted there.
import { API_BASE_URL } from '@/constants/api/config';
import { getAuthHeaders } from '@/services/walletApi';

export type ImageUploadResult = { url: string } | { error: string };

export async function uploadImageFile(uri: string, namePrefix = 'image'): Promise<ImageUploadResult> {
  try {
    const form = new FormData();
    // RN FormData accepts the file-descriptor object shape.
    form.append('image', { uri, name: `${namePrefix}-${Date.now()}.jpg`, type: 'image/jpeg' } as any);
    const headers = await getAuthHeaders();
    delete headers['Content-Type']; // let RN set the multipart boundary
    const res = await fetch(`${API_BASE_URL}/upload`, { method: 'POST', headers, body: form as any });
    const json = await res.json();
    if (json?.success && json.data?.url) return { url: json.data.url as string };
    return { error: json?.message || 'Upload failed. Please try again.' };
  } catch (err: any) {
    return { error: err?.message || 'Upload failed. Please try again.' };
  }
}
