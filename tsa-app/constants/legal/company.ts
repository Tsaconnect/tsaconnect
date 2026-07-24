// Single source of truth for the operating entity's details. Referenced by the
// legal documents, the Settings support row, and anywhere else the app names
// the company — so a change of address or support mailbox lands in one place.
//
// Kept in sync with tsa-landing's src/data/content.ts.

export const COMPANY = {
  legalName: 'Obaino4Christ Business International Connectors Ltd',
  registrationNumber: 'RC 1897254',
  jurisdiction: 'Federal Republic of Nigeria',
  productName: 'TSA Connect',
  domain: 'tsaconnectworld.com',
  website: 'https://tsaconnectworld.com',
  supportEmail: 'support@tsaconnectworld.com',
  privacyEmail: 'support@tsaconnectworld.com',
  founderEmail: 'obic@tsaconnectworld.com',
} as const;

export const LEGAL_URLS = {
  privacy: 'https://tsaconnectworld.com/privacy',
  terms: 'https://tsaconnectworld.com/terms',
} as const;
