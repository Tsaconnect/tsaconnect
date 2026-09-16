import { COMPANY, LEGAL_URLS } from './company';
import type { LegalDocument } from './types';

// Keep the text, version and effectiveDate identical to the published page at
// tsaconnectworld.com/privacy — the stores check that the in-app policy and the
// listed policy URL agree.

export const PRIVACY_POLICY: LegalDocument = {
  title: 'Privacy Policy',
  version: '1.0',
  effectiveDate: '24 July 2026',
  url: LEGAL_URLS.privacy,
  intro: [
    `This Privacy Policy explains how ${COMPANY.legalName} (${COMPANY.registrationNumber}) ("we", "us", "our") collects, uses, shares and protects your personal information when you use the ${COMPANY.productName} mobile application and related services (the "App").`,
    `We are the data controller for the personal information described in this policy. If you do not agree with this policy, please do not use the App.`,
  ],
  sections: [
    {
      heading: '1. A note on your crypto wallet',
      body: [
        'The wallet in the App is non-custodial. Your recovery phrase and private keys are generated on your device and stored in your device’s secure storage. We never receive, transmit or store them, and we cannot recover them for you.',
        'This has two consequences you should understand. First, we cannot freeze, reverse or restore on-chain funds. Second, deleting your account does not delete your wallet or your funds — as long as you keep your recovery phrase, you retain full control of the assets.',
        'Blockchain transactions are public and permanent. Any transaction you broadcast is recorded on a public ledger that we do not control and cannot amend or erase.',
      ],
    },
    {
      heading: '2. Information you give us',
      bullets: [
        'Account details: name, username, email address, phone number, password (stored only as a salted hash), and country.',
        'Profile details: profile photo, address, city and state where you provide them.',
        'Identity verification (KYC): your Bank Verification Number (BVN) or National Identification Number (NIN), identity documents, and a selfie or short video. KYC is required for the products and services marketplace and for our payment services (P2P, Payment Merchant, Instant Transfer and Instant Pay). It is not required for on-chain spot trading or token swaps.',
        'Financial details: bank account number, bank name and account name used for fiat settlement and payouts.',
        'Merchant details: business name, description, registered address, registration number and settlement account, where you apply to become a merchant.',
        'Content you submit: product and service listings, images, order and dispute messages, proof-of-payment uploads, ratings and reviews.',
        'Support correspondence: the contents of messages you send us.',
      ],
    },
    {
      heading: '3. Information we collect automatically',
      bullets: [
        'Public wallet addresses you register with the App, and the transaction history associated with them.',
        'Device and technical data: device model, operating system version, app version, language, and a push notification token if you enable notifications.',
        'Usage and security data: IP address, login timestamps, failed login attempts and similar records we keep to detect fraud and abuse.',
      ],
      footnote: [
        'The App does not use third-party advertising networks and does not track you across other companies’ apps or websites.',
      ],
    },
    {
      heading: '4. How we use your information',
      bullets: [
        'To create and operate your account and provide the features you use.',
        'To verify your identity and meet our anti-money-laundering, counter-terrorist-financing and know-your-customer obligations.',
        'To process marketplace orders, escrow releases, P2P trades and fiat payouts.',
        'To detect, investigate and prevent fraud, abuse and prohibited activity, and to enforce our Terms of Service.',
        'To send you service messages about your account, transactions, trades, orders and disputes.',
        'To provide customer support and resolve disputes between buyers, sellers and traders.',
        'To improve reliability and performance, and to diagnose faults.',
        'To comply with applicable law and respond to lawful requests from regulators, courts and law-enforcement agencies.',
      ],
    },
    {
      heading: '5. Legal bases for processing',
      body: [
        'Where the Nigeria Data Protection Act and the NDPR apply, and where the EU/UK GDPR applies to you, we rely on the following legal bases:',
      ],
      bullets: [
        'Performance of a contract — to provide the App and the services you ask for.',
        'Legal obligation — for KYC, anti-money-laundering, tax and record-keeping duties.',
        'Legitimate interests — to secure the platform, prevent fraud, and improve our services, balanced against your rights.',
        'Consent — for push notifications and for optional features such as camera and photo access. You can withdraw consent at any time in your device settings.',
      ],
    },
    {
      heading: '6. Who we share information with',
      body: [
        'We do not sell your personal information. We share it only as described below, and only to the extent needed:',
      ],
      bullets: [
        'Identity verification providers, to confirm your BVN or NIN and check your documents.',
        'Licensed payment and settlement partners, to process fiat collections and payouts to your bank account.',
        'Blockchain infrastructure and liquidity providers, to read balances, broadcast transactions and obtain swap quotes. These receive wallet addresses and transaction data, not your identity documents.',
        'Cloud hosting, media storage, email delivery and push notification providers that operate our infrastructure under contract.',
        'Other users, but only the limited profile information needed to transact — for example, a counterparty in a P2P trade sees your display name, trade statistics and the payment details you choose to share for that trade.',
        'Regulators, law-enforcement agencies, courts and professional advisers, where we are legally required or permitted to disclose.',
        'A successor entity, if we are involved in a merger, acquisition or sale of assets. We will notify you before your information becomes subject to a different privacy policy.',
      ],
    },
    {
      heading: '7. International transfers',
      body: [
        'Some of our service providers operate outside Nigeria. Where we transfer your personal information abroad, we do so under the safeguards required by the Nigeria Data Protection Act — typically standard contractual clauses or a transfer to a country recognised as providing adequate protection.',
      ],
    },
    {
      heading: '8. How long we keep your information',
      bullets: [
        'Account and profile data: for as long as your account is open.',
        'After you delete your account: your account is closed immediately and we retain a limited record for 30 days so that we can reverse the deletion if you ask us to, after which we erase or anonymise it.',
        'KYC records and transaction history: retained for at least 5 years after the end of our relationship with you, because anti-money-laundering law requires it. This obligation outlives your account and survives a deletion request.',
        'Support correspondence: up to 2 years.',
        'On-chain data: permanent and outside our control.',
      ],
    },
    {
      heading: '9. Your rights',
      body: [
        'Subject to the retention duties described above, you have the right to:',
      ],
      bullets: [
        'Access the personal information we hold about you and receive a copy.',
        'Correct information that is inaccurate or incomplete — most of this you can edit yourself in the App.',
        'Delete your account and the personal information we are not legally required to keep. You can do this yourself in the App under Settings → Delete Account.',
        'Object to or restrict certain processing, and withdraw consent where we rely on it.',
        'Receive your data in a portable, machine-readable format.',
        'Complain to the Nigeria Data Protection Commission, or to your local supervisory authority if you are in the EU or UK.',
      ],
    },
    {
      heading: '10. Deleting your account',
      body: [
        'You can delete your account at any time from Settings → Delete Account. You will be asked to confirm your password. You cannot delete while you have an open P2P trade or an in-flight marketplace order, because another user’s money may be held in escrow against you — settle or cancel those first.',
        'When you confirm, your account is closed immediately, you are signed out, and your devices stop receiving notifications. Our team is notified so the permanent erasure can be completed after the 30-day window. If you change your mind inside that window, contact ' + COMPANY.supportEmail + ' and we can reinstate the account.',
        'Deleting your account does not affect your non-custodial wallet. Keep your recovery phrase — it, not your account, controls your funds.',
      ],
    },
    {
      heading: '11. Security',
      body: [
        'We protect your information with encryption in transit, hashed passwords, access controls on our systems, and optional PIN and biometric locks on the App itself. Your wallet recovery phrase is held in your device’s secure enclave or keystore.',
        'No system is perfectly secure. You are responsible for keeping your password, PIN and recovery phrase confidential, and for the security of the device you use.',
      ],
    },
    {
      heading: '12. Children',
      body: [
        'The App is not intended for anyone under 18, and we do not knowingly collect information from children. If you believe a child has provided us with personal information, contact us and we will delete it.',
      ],
    },
    {
      heading: '13. Changes to this policy',
      body: [
        'We may update this policy. When we make a material change we will update the version and effective date above and notify you in the App or by email before the change takes effect. Continuing to use the App after that means you accept the updated policy.',
      ],
    },
    {
      heading: '14. Contact us',
      body: [
        `${COMPANY.legalName} (${COMPANY.registrationNumber}), ${COMPANY.jurisdiction}.`,
        `Privacy and data protection enquiries: ${COMPANY.privacyEmail}`,
        `General support: ${COMPANY.supportEmail}`,
        `Website: ${COMPANY.website}`,
      ],
    },
  ],
};
