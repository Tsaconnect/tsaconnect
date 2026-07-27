import React from 'react';
import LegalDocumentScreen from '@/components/legal/LegalDocumentScreen';
import { PRIVACY_POLICY } from '@/constants/legal';

export default function PrivacyPolicyScreen() {
  return <LegalDocumentScreen document={PRIVACY_POLICY} />;
}
