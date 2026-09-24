import { useId } from 'react';
import { Copy } from 'lucide-react';
import { copyText } from '@/lib/clipboard';

/** A titled code sample on the dark code surface, with Copy. No highlighter: plain text reads fine and ships nothing. */
export function CodeBlock({ title, code }: { title: string; code: string }) {
  const titleId = useId();
  return (
    <figure aria-labelledby={titleId} className="m-0 min-w-0 overflow-hidden rounded-md border border-slate-line bg-slate-950">
      <figcaption className="flex items-center gap-3 border-b border-slate-line py-1.5 pr-2 pl-4">
        <span id={titleId} className="min-w-0 flex-1 truncate text-[12.5px] font-semibold text-on-slate-muted">{title}</span>
        <button
          type="button"
          onClick={() => copyText(code)}
          aria-label={`Copy ${title}`}
          className="inline-flex shrink-0 items-center gap-1.5 rounded-sm px-2 py-1 text-[12px] font-semibold text-on-slate-muted transition-colors hover:bg-slate-elevated hover:text-white"
        >
          <Copy className="size-3.5" aria-hidden />
          Copy
        </button>
      </figcaption>
      {/* Focusable so a keyboard can scroll a long line into view. */}
      <pre tabIndex={0} className="m-0 overflow-x-auto p-4 font-mono text-[12.5px] leading-[1.65] text-on-slate">
        <code>{code}</code>
      </pre>
    </figure>
  );
}
