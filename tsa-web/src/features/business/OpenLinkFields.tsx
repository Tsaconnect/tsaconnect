import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Segmented } from '@/components/ui/segmented';
import { paymentExpiryOptions, type OpenLinkDraft, type OpenLinkErrors } from './openLinkForm';

/** A reusable link's label, optional limits and how long each payment stays open. */
export function OpenLinkFields({ draft, errors, set }: {
  draft: OpenLinkDraft;
  errors: OpenLinkErrors;
  set: (field: keyof OpenLinkDraft) => (value: string) => void;
}) {
  return (
    <>
      <Field label="Label" hint="What your customers see, e.g. Mama Ngozi Store — Shop 12. It's on the poster." error={errors.label}>
        <Input maxLength={80} autoComplete="off" value={draft.label} onChange={(e) => set('label')(e.target.value)} />
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Minimum (₦, optional)" hint="Blank: your account's limit." error={errors.min}>
          <Input inputMode="decimal" value={draft.min} onChange={(e) => set('min')(e.target.value)} />
        </Field>
        <Field label="Maximum (₦, optional)" hint="Blank: your account's limit." error={errors.max}>
          <Input inputMode="decimal" value={draft.max} onChange={(e) => set('max')(e.target.value)} />
        </Field>
      </div>
      <div className="grid gap-1.5">
        <p aria-hidden className="text-[13px] leading-[18px] font-semibold text-text-secondary">Each payment expires after</p>
        <Segmented
          legend="Each payment expires after"
          name="paymentExpiresIn"
          value={draft.paymentExpiresIn}
          options={paymentExpiryOptions(draft.paymentExpiresIn)}
          onChange={set('paymentExpiresIn')}
        />
        <p className="text-caption text-text-secondary">Counted from when a customer enters the amount.</p>
      </div>
    </>
  );
}
