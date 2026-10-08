const GENERIC = "Couldn't export. Try again.";
// Safari can cancel a download whose object URL is revoked as the click returns.
const REVOKE_AFTER_MS = 1000;

/** The .csv filename a Content-Disposition header names, made safe to save as, or undefined. */
function dispositionName(header: string | null): string | undefined {
  const raw = /filename="?([^";]+)"?/i.exec(header ?? '')?.[1];
  // eslint-disable-next-line no-control-regex
  const name = raw?.split(/[/\\]/).pop()?.replace(/[\\/:*?"<>|\u0000-\u001f\u007f]/g, '').trim();
  return name && /.\.csv$/i.test(name) ? name : undefined;
}

/**
 * Downloads a same-origin file. Fetched, not opened as a link, so a refusal (the
 * API answers 422 or 429 with a JSON message) can be shown rather than saved as
 * a file, and so Safari has no new tab to block after an earlier await. The
 * file is named by the response's Content-Disposition when that is a plain .csv
 * name, else `fallbackName`. Throws an Error with a message fit to show.
 */
export async function downloadFile(url: string, fallbackName: string): Promise<void> {
  let res: Response;
  try {
    res = await fetch(url, { credentials: 'same-origin' });
  } catch {
    throw new Error(GENERIC);
  }
  if (!res.ok) {
    const body = (await res.json().catch(() => null)) as { message?: unknown } | null;
    throw new Error(typeof body?.message === 'string' && body.message ? body.message : GENERIC);
  }
  const href = URL.createObjectURL(await res.blob());
  const a = document.createElement('a');
  try {
    a.href = href;
    a.download = dispositionName(res.headers.get('Content-Disposition')) ?? fallbackName;
    document.body.append(a);
    a.click();
  } catch (err) {
    URL.revokeObjectURL(href);
    throw err;
  } finally {
    a.remove();
  }
  setTimeout(() => URL.revokeObjectURL(href), REVOKE_AFTER_MS);
}
