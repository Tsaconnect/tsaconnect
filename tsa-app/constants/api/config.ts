// Centralized API configuration. All requests go to the deployed API; the
// server's .env controls which network (testnet/mainnet) is active, so we
// don't need a dev/prod toggle in the app.

export const API_BASE_URL = 'https://tsa.mcgpchain.com/api';

export const baseUrl = API_BASE_URL;

/** React Native's fetch (OkHttp on Android) has no timeout of its own: a hung request would wait forever. */
export const REQUEST_TIMEOUT_MS = 30_000;

/**
 * The code on an error for a request that never got an answer — no connection,
 * or no reply within REQUEST_TIMEOUT_MS. For a POST that starts something, the
 * server may have acted anyway: callers treat it as "unknown", never as
 * "failed, try again". Here, with no imports, so any layer can read it.
 */
export const TRANSPORT_ERROR = 'transport';
