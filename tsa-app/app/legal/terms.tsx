import React from 'react';
import LegalDocumentScreen from '@/components/legal/LegalDocumentScreen';
import { TERMS_OF_SERVICE } from '@/constants/legal';

export default function TermsOfServiceScreen() {
  return <LegalDocumentScreen document={TERMS_OF_SERVICE} />;
}
