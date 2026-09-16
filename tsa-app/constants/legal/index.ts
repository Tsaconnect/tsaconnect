// SYNC NOTE
//
// This directory is mirrored byte-for-byte into
// tsa-landing/src/data/legal/, which publishes the same documents at the URLs
// the store listings point to. The stores check that the in-app policy and the
// listed policy agree, so the two copies must not drift. After changing
// anything here, bump `version` + `effectiveDate` and copy the directory over:
//
//   cp tsa-app/constants/legal/*.ts ../../tsa-landing/src/data/legal/

export { COMPANY, LEGAL_URLS } from './company';
export { PRIVACY_POLICY } from './privacy';
export { TERMS_OF_SERVICE } from './terms';
export type { LegalDocument, LegalSection } from './types';
