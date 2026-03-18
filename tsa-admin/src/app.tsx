import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Toaster } from 'sonner';
import { AuthProvider } from '@/contexts/auth-context';
import { useAuth } from '@/hooks/use-auth';
import { AppLayout } from '@/components/layout/app-layout';
import LoginPage from '@/pages/auth/login';
import DashboardPage from '@/pages/dashboard/index';
import UsersPage from '@/pages/users/index';
import ProductsPage from '@/pages/products/index';
import CategoriesPage from '@/pages/categories/index';
import OrdersPage from '@/pages/orders/index';
import OrderDetailPage from '@/pages/orders/[id]';
import AdvertRequestsPage from '@/pages/advert-requests/index';
import DepositsPage from '@/pages/deposits/index';
import VerificationsPage from '@/pages/verifications/index';
import MerchantRequestsPage from '@/pages/merchant-requests/index';
import SettingsPage from '@/pages/settings/index';

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: 1,
      staleTime: 30 * 1000,
    },
  },
});

function ProtectedRoute({ children }: { children: React.ReactNode }) {
  const { user, isLoading } = useAuth();
  const token = sessionStorage.getItem('authToken') || localStorage.getItem('authToken');
  if (isLoading) return null;
  if (!token || !user) return <Navigate to="/login" replace />;
  return <>{children}</>;
}

export default function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <AuthProvider>
        <BrowserRouter>
          <Routes>
            <Route path="/login" element={<LoginPage />} />
            <Route path="/" element={<ProtectedRoute><AppLayout /></ProtectedRoute>}>
              <Route index element={<Navigate to="/dashboard" replace />} />
              <Route path="dashboard" element={<DashboardPage />} />
              <Route path="users" element={<UsersPage />} />
              <Route path="products" element={<ProductsPage />} />
              <Route path="categories" element={<CategoriesPage />} />
              <Route path="orders" element={<OrdersPage />} />
              <Route path="orders/:id" element={<OrderDetailPage />} />
              <Route path="advert-requests" element={<AdvertRequestsPage />} />
              <Route path="deposits" element={<DepositsPage />} />
              <Route path="verifications" element={<VerificationsPage />} />
              <Route path="merchant-requests" element={<MerchantRequestsPage />} />
              <Route path="settings" element={<SettingsPage />} />
            </Route>
          </Routes>
        </BrowserRouter>
        <Toaster position="top-right" richColors />
      </AuthProvider>
    </QueryClientProvider>
  );
}
