import { useEffect, useState, type ReactNode } from 'react';
import { useLocation } from 'react-router-dom';
import * as D from '@radix-ui/react-dialog';
import { Sidebar, type ShellMerchant, type ShellSection } from './Sidebar';
import { TopBar } from './TopBar';
import { modeOf } from './mode';

// Tailwind's `lg` breakpoint.
const LG = '(min-width: 64rem)';

/**
 * The signed-in frame: dark sidebar beside a top bar and the page. From `lg` the
 * sidebar is a fixed column; below it, an off-canvas panel behind the menu button.
 * `merchant` feeds the sidebar's business card: undefined while loading, null for none.
 */
export function AppShell({ section, title, actions, merchant, children }: {
  section: ShellSection;
  title: string;
  actions?: ReactNode;
  merchant?: ShellMerchant | null;
  children: ReactNode;
}) {
  const { pathname } = useLocation();
  const [menuOpen, setMenuOpen] = useState(false);
  const mode = section === 'business' ? modeOf(pathname) : null;
  // Widening past `lg` shows the fixed sidebar; an open off-canvas copy (and its
  // scroll lock) must not linger behind it.
  useEffect(() => {
    if (!menuOpen || typeof window.matchMedia !== 'function') return;
    const wide = window.matchMedia(LG);
    const onChange = (e: MediaQueryListEvent) => {
      if (e.matches) setMenuOpen(false);
    };
    wide.addEventListener('change', onChange);
    return () => wide.removeEventListener('change', onChange);
  }, [menuOpen]);

  const sidebar = (onNavigate?: () => void) => <Sidebar section={section} mode={mode ?? 'live'} merchant={merchant} onNavigate={onNavigate} />;

  return (
    <div className="min-h-full lg:grid lg:grid-cols-[248px_minmax(0,1fr)]">
      <aside className="sticky top-0 hidden h-screen lg:block">{sidebar()}</aside>

      <D.Root open={menuOpen} onOpenChange={setMenuOpen}>
        <D.Portal>
          <D.Overlay className="fixed inset-0 z-40 bg-[var(--overlay)] lg:hidden" />
          <D.Content aria-describedby={undefined} className="fixed inset-y-0 left-0 z-50 w-[272px] max-w-[85vw] shadow-[var(--shadow-lg)] focus:outline-none lg:hidden">
            <D.Title className="sr-only">Menu</D.Title>
            {sidebar(() => setMenuOpen(false))}
          </D.Content>
        </D.Portal>
      </D.Root>

      <div className="flex min-w-0 flex-col">
        <div className="sticky top-0 z-30">
          {mode === 'test' && (
            <div className="bg-[repeating-linear-gradient(135deg,#F7E3B0_0_10px,#F3D995_10px_20px)] px-4 py-1.5 text-center text-[12.5px] font-semibold text-[#6B4E0E]">
              Test mode — no real money moves
            </div>
          )}
          <TopBar title={title} actions={actions} mode={mode} onMenu={() => setMenuOpen(true)} />
        </div>
        <main className="w-full max-w-[1240px] px-4 pt-6 pb-10 sm:px-7 sm:pt-[26px]">{children}</main>
      </div>
    </div>
  );
}
