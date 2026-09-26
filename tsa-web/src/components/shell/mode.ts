import { useParams } from 'react-router-dom';

export type Mode = 'live' | 'test';

const BUSINESS = /^\/business\/([^/]+)(\/.*)?$/;

/** The mode in a /business/:mode/... path, or null outside the business area. */
export function modeOf(pathname: string): Mode | null {
  const m = BUSINESS.exec(pathname);
  if (!m) return null;
  return m[1] === 'test' ? 'test' : 'live';
}

/**
 * The same business page in the other mode (/business/live/payments →
 * /business/test/payments). A payment and a page of the list belong to one
 * mode, so the other mode opens its list at the first page, filters kept.
 */
export function withMode(pathname: string, search: string, mode: Mode): string {
  const page = (BUSINESS.exec(pathname)?.[2] ?? '/overview').replace(/^\/payments\/.*$/, '/payments');
  const params = new URLSearchParams(search);
  params.delete('page');
  const query = params.toString();
  return `/business/${mode}${page}${query ? `?${query}` : ''}`;
}

/** Business pages render only under a checked :mode (ModeGuard), so anything else is live. */
export function useMode(): Mode {
  return useParams().mode === 'test' ? 'test' : 'live';
}
