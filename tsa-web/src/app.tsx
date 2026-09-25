import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { BrowserRouter } from 'react-router-dom';
import { ApiError } from '@/lib/api';
import { SessionProvider } from '@/session/session';
import { Toaster } from '@/components/ui/toaster';
import { ProdApiBar } from '@/components/shell/ProdApiBar';
import { AppRoutes } from './routes';

// A dropped connection, a timeout (status 0) or a 5xx may pass on retry; a 4xx
// will not (and a 401 was already refreshed by api()).
const queryClient = new QueryClient({
  defaultOptions: {
    queries: { retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2, refetchOnWindowFocus: false },
  },
});

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ProdApiBar />
      <BrowserRouter>
        <SessionProvider>
          <AppRoutes />
          <Toaster position="bottom-right" />
        </SessionProvider>
      </BrowserRouter>
    </QueryClientProvider>
  );
}
