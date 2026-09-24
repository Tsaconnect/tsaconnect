import { clsx, type ClassValue } from 'clsx';
import { extendTailwindMerge } from 'tailwind-merge';

// The TSA type scale adds font sizes (text-title3 …) that tailwind-merge would
// otherwise read as text colours and drop next to a text-text-* colour class.
const twMerge = extendTailwindMerge({
  extend: { theme: { text: ['display', 'title1', 'title2', 'title3', 'body', 'body-sm', 'caption', 'overline'] } },
});

export const cn = (...inputs: ClassValue[]) => twMerge(clsx(inputs));
