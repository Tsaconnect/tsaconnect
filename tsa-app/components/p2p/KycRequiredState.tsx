// The P2P "verify first" state, shared by the market (KYC 403 on /p2p/ads)
// and the create-ad screens. Branches on the machine code, never the message.
import React from 'react';
import { router } from 'expo-router';
import { EmptyState } from '@/components/ui';
import { KYC_LEGAL_NAME_MISSING, KYC_ROUTE } from '@/utils/kycRefusal';

export const KycRequiredState: React.FC<{ code?: string; purpose: 'market' | 'post' }> = ({ code, purpose }) =>
  code === KYC_LEGAL_NAME_MISSING ? (
    <EmptyState
      icon="info"
      title="Contact support"
      subtitle="Your identity is verified, but we couldn't confirm the legal name on it. Contact support to finish setting up P2P."
    />
  ) : (
    <EmptyState
      icon="lock"
      title="Verify your identity to use P2P"
      subtitle={purpose === 'post'
        ? 'Posting ads needs a verified identity. It only takes a few minutes.'
        : 'Buying and selling on P2P needs a verified identity. It only takes a few minutes.'}
      actionLabel="Verify now"
      onAction={() => router.push(KYC_ROUTE as never)}
    />
  );
