// utils/strictHttpsHost.ts
//
// The host of an https URL, read strictly — or null. React Native's own URL is
// a set of regexes that does not validate (it reads "https://evil.com#@verify.didit.me"
// as Didit's), while a browser reads a backslash as a slash
// ("https://evil.com\@verify.didit.me" is evil.com). So a backslash, whitespace,
// a user name or password, or any disagreement between URL and a strict reading
// of the authority is refused. Every "is this really our host?" check uses it.

/** The authority of an https URL, read strictly: a plain host name and an optional port, nothing else. */
const STRICT_HTTPS_HOST = /^https:\/\/([a-z0-9.-]+)(?::\d{1,5})?(?:[/?#]|$)/i;

export function strictHttpsHost(raw: string): string | null {
  if (/[\\\s]/.test(raw)) return null;
  let url: URL;
  try {
    url = new URL(raw);
  } catch {
    return null;
  }
  const strict = STRICT_HTTPS_HOST.exec(raw);
  const host = url.hostname.toLowerCase();
  if (url.protocol !== 'https:' || url.username || url.password || !strict || strict[1].toLowerCase() !== host) return null;
  return host;
}
