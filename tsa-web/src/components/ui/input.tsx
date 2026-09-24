import { forwardRef, useState, type InputHTMLAttributes } from 'react';
import { Eye, EyeOff } from 'lucide-react';
import { cn } from '@/lib/cn';
import { control } from './control';

export const Input = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(({ className, ...p }, ref) => (
  <input ref={ref} className={cn(control, className)} {...p} />
));
Input.displayName = 'Input';

/** A password input with a show/hide toggle; takes Field's id and aria props like Input. */
export const PasswordInput = forwardRef<HTMLInputElement, Omit<InputHTMLAttributes<HTMLInputElement>, 'type'>>(({ className, ...p }, ref) => {
  const [shown, setShown] = useState(false);
  const Icon = shown ? EyeOff : Eye;
  return (
    <div className="relative">
      <input ref={ref} type={shown ? 'text' : 'password'} className={cn(control, 'pr-11', className)} {...p} />
      <button
        type="button"
        onClick={() => setShown((s) => !s)}
        // A constant name with a pressed state; the name avoids "password" so a
        // label lookup for the field itself never also finds this button.
        aria-label="Show characters"
        aria-pressed={shown}
        className="absolute inset-y-0 right-0 grid w-11 place-items-center rounded-r-md text-control-icon hover:text-text-secondary"
      >
        <Icon className="size-4" aria-hidden />
      </button>
    </div>
  );
});
PasswordInput.displayName = 'PasswordInput';
