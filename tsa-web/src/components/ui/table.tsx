import type { HTMLAttributes, TdHTMLAttributes, ThHTMLAttributes } from 'react';
import { cn } from '@/lib/cn';

export const Table = ({ className, ...p }: HTMLAttributes<HTMLTableElement>) => (
  <div className="overflow-x-auto">
    <table className={cn('w-full border-collapse', className)} {...p} />
  </div>
);
export const THead = (p: HTMLAttributes<HTMLTableSectionElement>) => <thead {...p} />;
export const TBody = (p: HTMLAttributes<HTMLTableSectionElement>) => <tbody {...p} />;
export const TR = ({ className, ...p }: HTMLAttributes<HTMLTableRowElement>) => (
  <tr className={cn('border-b border-hairline last:border-0 [tbody_&]:hover:bg-[#FCFBF7]', className)} {...p} />
);
export const TH = ({ className, ...p }: ThHTMLAttributes<HTMLTableCellElement>) => (
  <th
    className={cn('whitespace-nowrap border-b border-hairline bg-background px-5 py-2.5 text-left text-overline font-semibold uppercase text-text-secondary', className)}
    {...p}
  />
);
export const TD = ({ className, ...p }: TdHTMLAttributes<HTMLTableCellElement>) => (
  <td className={cn('px-5 py-3 align-middle text-body-sm', className)} {...p} />
);
