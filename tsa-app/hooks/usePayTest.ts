// hooks/usePayTest.ts
//
// Approve/Decline for a test-mode payment on app/pay.tsx. `request` only
// opens the AuthorizationModal — nothing reaches the server until the payer
// actually authorizes (PIN/biometric), matching every other money-moving
// action in the app (see components/common/AuthorizationModal.tsx; do not
// add a second unlock path). `submitTestOutcome` fires from `onAuthorized`.
//
// AuthorizationProps is exported so Tasks 14 and 16 (live pay, and whatever
// authorizes next) can spread the same shape onto their own modal without
// redeclaring it.
import { useCallback, useState } from 'react';
import { PayError, submitTestOutcome, type PayCheckout } from '@/services/payApi';

export type AuthorizationProps = {
  visible: boolean;
  title: string;
  onAuthorized(): void;
  onCancel(): void;
};

export type UsePayTest = {
  request(outcome: 'approve' | 'decline'): void;
  authorization: AuthorizationProps;
  submitting: boolean;
  error?: string;
  result?: PayCheckout;
};

export function usePayTest(paymentId: string): UsePayTest {
  const [visible, setVisible] = useState(false);
  const [pendingOutcome, setPendingOutcome] = useState<'approve' | 'decline' | null>(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState<string | undefined>(undefined);
  const [result, setResult] = useState<PayCheckout | undefined>(undefined);

  const request = useCallback((outcome: 'approve' | 'decline') => {
    setError(undefined);
    setPendingOutcome(outcome);
    setVisible(true);
  }, []);

  const onCancel = useCallback(() => {
    setVisible(false);
    setPendingOutcome(null);
  }, []);

  const onAuthorized = useCallback(() => {
    setVisible(false);
    if (!pendingOutcome) return;
    const outcome = pendingOutcome;
    setPendingOutcome(null);
    setSubmitting(true);
    setError(undefined);
    submitTestOutcome(paymentId, outcome)
      .then((updated) => setResult(updated))
      .catch((err: unknown) => {
        setError(err instanceof PayError ? err.message : 'Something went wrong. Please try again.');
      })
      .finally(() => setSubmitting(false));
  }, [pendingOutcome, paymentId]);

  const authorization: AuthorizationProps = {
    visible,
    title: pendingOutcome === 'decline' ? 'Authorize decline' : 'Authorize payment',
    onAuthorized,
    onCancel,
  };

  return { request, authorization, submitting, error, result };
}
