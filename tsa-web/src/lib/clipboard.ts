import { toast } from '@/components/ui/toaster';

const COPY_FAILED = "Couldn't copy — select it and copy it by hand.";

/** Copies text and says whether it worked (a toast either way). */
export function copyText(value: string) {
  if (!navigator.clipboard) {
    toast.error(COPY_FAILED);
    return;
  }
  navigator.clipboard.writeText(value).then(
    () => toast.success('Copied'),
    () => toast.error(COPY_FAILED),
  );
}
