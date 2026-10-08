const GENERIC = "Couldn't export. Try again.";

/** The filename a Content-Disposition header names, if it names one. */
const dispositionName = (header: string | null) => /filename="?([^";]+)"?/i.exec(header ?? '')?.[1];

/**
 * Downloads a same-origin file. Fetched, not opened as a link, so a refusal (the
 * API answers 422 or 429 with a JSON message) can be shown rather than saved as
 * a file, and so Safari has no new tab to block after an earlier await. The
 * file is named by the response's Content-Disposition, else `fallbackName`.
 * Throws an Error carrying the API's message when the answer is a refusal.
 */
export async function downloadFile(url: string, fallbackName: string): Promise<void> {
  const res = await fetch(url, { credentials: 'same-origin' });
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { message?: unknown } | null;
    throw new Error(typeof body?.message === 'string' && body.message ? body.message : GENERIC);
  }
  const href = URL.createObjectURL(await res.blob());
  try {
    const a = document.createElement('a');
    a.href = href;
    a.download = dispositionName(res.headers.get('Content-Disposition')) ?? fallbackName;
    document.body.append(a);
    a.click();
    a.remove();
  } finally {
    URL.revokeObjectURL(href);
  }
}
