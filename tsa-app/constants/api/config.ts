// Centralized API configuration. All requests go to the deployed API; the
// server's .env controls which network (testnet/mainnet) is active, so we
// don't need a dev/prod toggle in the app.

export const API_BASE_URL = 'https://tsa.mcgpchain.com/api';

export const baseUrl = API_BASE_URL;
