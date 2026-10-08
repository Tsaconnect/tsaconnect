/**
 * Opens a same-origin file in a new tab. It is a link click, not fetch, so the
 * browser streams it to disk with the session cookie, however large it is. There
 * is no `download` attribute: the response's Content-Disposition makes a file
 * download, while a JSON refusal shows up readable instead of as a file.
 */
export function downloadFile(url: string): void {
  const a = document.createElement('a');
  a.href = url;
  a.target = '_blank';
  a.rel = 'noopener';
  document.body.append(a);
  a.click();
  a.remove();
}
