// Centralized API configuration
// All API URLs should be imported from here

const DEV_API_URL = 'https://tsa.mcgpchain.com/api';
const PROD_API_URL = 'https://tsa.mcgpchain.com/api';

// Toggle this for local development vs production
const IS_DEV = __DEV__;

export const API_BASE_URL = IS_DEV ? DEV_API_URL : PROD_API_URL;

// Re-export as baseUrl for backward compatibility with existing imports
export const baseUrl = API_BASE_URL;
