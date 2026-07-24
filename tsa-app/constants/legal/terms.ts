import { COMPANY, LEGAL_URLS } from './company';
import type { LegalDocument } from './types';

// Keep the text, version and effectiveDate identical to the published page at
// tsaconnectworld.com/terms.

export const TERMS_OF_SERVICE: LegalDocument = {
  title: 'Terms of Service',
  version: '1.0',
  effectiveDate: '24 July 2026',
  url: LEGAL_URLS.terms,
  intro: [
    `These Terms of Service ("Terms") are a binding agreement between you and ${COMPANY.legalName} (${COMPANY.registrationNumber}) ("we", "us", "our"), governing your use of the ${COMPANY.productName} mobile application and related services (the "App").`,
    'By creating an account or using the App, you confirm that you accept these Terms. If you do not accept them, do not use the App.',
  ],
  sections: [
    {
      heading: '1. Eligibility',
      bullets: [
        'You must be at least 18 years old and legally able to enter into a contract.',
        'You must not be resident in, or acting on behalf of anyone in, a country subject to comprehensive sanctions, and you must not appear on any applicable sanctions list.',
        'You are responsible for checking that your use of the App is lawful where you live. Some features are unavailable in some countries.',
        'You may hold one account. Creating multiple accounts to evade limits, rewards rules or a suspension is a breach of these Terms.',
      ],
    },
    {
      heading: '2. Your account',
      bullets: [
        'You must give accurate information when you register and keep it up to date.',
        'You are responsible for everything that happens under your account, and for keeping your password, PIN, biometrics and device secure.',
        'Tell us immediately at ' + COMPANY.supportEmail + ' if you suspect unauthorised access.',
        'We may require identity verification (KYC) before you can use the marketplace or our payment services, and may set or change limits based on your verification level.',
      ],
    },
    {
      heading: '3. The non-custodial wallet — read this carefully',
      body: [
        'The wallet in the App is non-custodial. Your recovery phrase and private keys are generated on your device and never leave it. We do not hold them, cannot access your funds, and cannot act on your behalf on-chain.',
      ],
      bullets: [
        'If you lose your recovery phrase, your funds are permanently unrecoverable. We cannot reset it, restore it or help you regain access. Back it up offline.',
        'Blockchain transactions are irreversible. We cannot cancel, reverse or refund a transaction once it is broadcast, including one sent to a wrong address or on the wrong network.',
        'You are responsible for network (gas) fees and for choosing the correct network and asset.',
        'We are not a bank, an exchange operator of your assets, or a custodian. We do not hold client money in respect of your on-chain wallet.',
      ],
    },
    {
      heading: '4. Crypto risk disclosure',
      body: [
        'Digital assets are volatile and speculative. Their value can fall as well as rise, and you can lose the entire value of your holdings.',
        'Nothing in the App is investment, tax or legal advice. Quotes, rates and token information are provided for convenience, may be delayed or inaccurate, and are not an offer or a recommendation. You are solely responsible for your trading decisions and for any tax you owe.',
        'Smart contracts, blockchains and third-party bridges can fail, be exploited, or become congested. Where the App interacts with them we do so on a best-effort basis and do not guarantee the outcome.',
      ],
    },
    {
      heading: '5. Marketplace (products and services)',
      bullets: [
        'We provide the platform. Contracts for goods and services are between the buyer and the merchant; we are not a party to them and do not own, inspect or warrant anything listed.',
        'Merchants are responsible for the accuracy of their listings, for having the right to sell, for delivery, and for complying with the law that applies to what they sell.',
        'Buyers must pay for what they order and confirm receipt promptly once delivered.',
        'Payments for eligible orders are held in escrow and released on the terms shown at checkout.',
        'Prohibited listings include: illegal goods and services, weapons, controlled drugs, stolen property, counterfeit items, adult content, and anything infringing another party’s rights. We may remove listings and suspend accounts for breach.',
      ],
    },
    {
      heading: '6. P2P trading and escrow',
      bullets: [
        'P2P trades are between users. The seller’s digital assets are locked in an on-chain escrow contract; the fiat leg is settled directly between the two users through the payment method they agree.',
        'A buyer must mark a trade as paid only after genuinely sending payment. A seller must release only after confirming receipt in their own bank account. Falsely marking a payment is fraud and we will act on it.',
        'Trades have a payment window. An unpaid trade may be cancelled automatically when the window expires and the assets returned to the seller.',
        'If a trade is disputed, our team will review the evidence both sides submit and may direct the escrow outcome. You agree to co-operate and to provide evidence honestly. Our dispute decision is final as between us and you as regards our platform, and does not prevent you from pursuing the other user through any legal route available to you.',
        'We are not responsible for chargebacks, bank reversals or fraud carried out by your counterparty on the fiat leg.',
      ],
    },
    {
      heading: '7. Fiat payment services',
      body: [
        'Fiat collections and payouts are provided through appropriately licensed payment partners. Their own terms apply to that leg of the transaction, and their processing times, limits and verification requirements are outside our control.',
        'You must only use bank accounts and payment methods that belong to you and are in the name on your verified account. Third-party payments may be rejected or reversed.',
      ],
    },
    {
      heading: '8. Fees',
      bullets: [
        'The fees that apply to a transaction are displayed before you confirm it. By confirming, you accept them.',
        'Merchant fees are deducted from the merchant’s settlement. Blockchain network fees are charged in addition and are set by the network, not by us.',
        'We may change our fees. Changes take effect when published in the App and apply to transactions made after that.',
      ],
    },
    {
      heading: '9. Rewards and incentive programmes',
      bullets: [
        'Any rewards, trading points, cashback or referral programme is discretionary and promotional. It is not a security, a deposit, an investment product, or a promise of future value.',
        'We may vary, suspend or end a programme, and we may adjust or withhold rewards obtained through self-referral, fake accounts, wash trading or any other manipulation.',
        'Rewards may be subject to eligibility checks, including completed identity verification.',
      ],
    },
    {
      heading: '10. Acceptable use',
      body: ['You must not:'],
      bullets: [
        'Use the App for money laundering, terrorist financing, sanctions evasion, fraud, or any other unlawful purpose.',
        'Impersonate anyone, or use another person’s identity documents or bank account.',
        'Manipulate prices, wash trade, or use bots or scripts to gain an unfair advantage.',
        'Probe, scan, overload or attempt to gain unauthorised access to the App or its infrastructure.',
        'Reverse engineer, decompile or copy the App except to the extent the law expressly permits.',
        'Harass, threaten or abuse other users or our staff.',
      ],
    },
    {
      heading: '11. Suspension and termination',
      body: [
        'We may suspend or close your account, or restrict features, if we reasonably believe you have breached these Terms, if we are required to by law or a regulator, or if we detect fraud or a security risk. Where we can lawfully do so, we will tell you why.',
        'You may close your account at any time from Settings → Delete Account, provided you have no open trades or orders. Closing your account does not affect your non-custodial wallet or obligations you have already incurred.',
        'Sections that by their nature should survive termination — including fees owed, liability, indemnity and governing law — continue to apply.',
      ],
    },
    {
      heading: '12. Intellectual property',
      body: [
        `The App, its name, logo, design and software are owned by ${COMPANY.legalName} and protected by intellectual property law. You get a personal, non-exclusive, non-transferable, revocable licence to use the App for its intended purpose.`,
        'You keep ownership of the content you upload, and grant us a licence to host, display and distribute it as needed to operate the App.',
      ],
    },
    {
      heading: '13. Disclaimers',
      body: [
        'The App is provided "as is" and "as available". To the fullest extent the law allows, we exclude all implied warranties, including merchantability, fitness for a particular purpose and non-infringement.',
        'We do not warrant that the App will be uninterrupted, timely, secure or error-free, that rates and quotes will be accurate, or that any blockchain, bridge or third-party provider will perform as expected.',
      ],
    },
    {
      heading: '14. Limitation of liability',
      body: [
        'To the fullest extent permitted by law, we are not liable for indirect, incidental, special, consequential or punitive damages, or for loss of profits, revenue, data, goodwill or digital assets.',
        'We are not liable for: loss of your recovery phrase or private keys; transactions you send in error; the acts or omissions of another user, a merchant, or a third-party payment, blockchain or liquidity provider; or losses caused by market movements.',
        'Where liability cannot lawfully be excluded, our total aggregate liability to you is limited to the greater of the fees you paid us in the three months before the event giving rise to the claim, or USD 100.',
        'Nothing in these Terms excludes liability for fraud, for death or personal injury caused by negligence, or for anything else that cannot lawfully be excluded.',
      ],
    },
    {
      heading: '15. Indemnity',
      body: [
        'You agree to indemnify us against claims, losses and reasonable costs arising from your breach of these Terms, your misuse of the App, or your infringement of anyone else’s rights.',
      ],
    },
    {
      heading: '16. Changes to these Terms',
      body: [
        'We may update these Terms. When we make a material change we will update the version and effective date above and notify you in the App or by email before it takes effect. If you continue to use the App afterwards, you accept the updated Terms; if you do not accept them, close your account.',
      ],
    },
    {
      heading: '17. Governing law and disputes',
      body: [
        `These Terms are governed by the laws of the ${COMPANY.jurisdiction}, and the courts of Nigeria have exclusive jurisdiction, subject to any mandatory consumer-protection rights you have where you live.`,
        `Before starting proceedings, please contact us at ${COMPANY.supportEmail} so we can try to resolve the matter directly. Most complaints are settled this way.`,
      ],
    },
    {
      heading: '18. General',
      bullets: [
        'If any provision is held unenforceable, the rest of the Terms continue in force.',
        'Our failure to enforce a provision is not a waiver of it.',
        'You may not assign your rights under these Terms; we may assign ours to a successor entity.',
        'These Terms, together with the Privacy Policy, are the entire agreement between us about the App.',
      ],
    },
    {
      heading: '19. Contact us',
      body: [
        `${COMPANY.legalName} (${COMPANY.registrationNumber}), ${COMPANY.jurisdiction}.`,
        `Support: ${COMPANY.supportEmail}`,
        `Website: ${COMPANY.website}`,
      ],
    },
  ],
};
