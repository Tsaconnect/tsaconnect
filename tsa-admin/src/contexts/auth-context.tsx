import { createContext, useState, useEffect, type ReactNode } from 'react';
import { getProfile } from '@/api/auth';
import type { User } from '@/types';

interface AuthState {
  user: User | null;
  token: string | null;
  isLoading: boolean;
  login: (token: string, rememberMe: boolean) => Promise<void>;
  logout: () => void;
}

export const AuthContext = createContext<AuthState | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [user, setUser] = useState<User | null>(null);
  const [token, setToken] = useState<string | null>(null);
  const [isLoading, setIsLoading] = useState(true);

  useEffect(() => {
    const stored = sessionStorage.getItem('authToken') || localStorage.getItem('authToken');
    if (stored) {
      setToken(stored);
      getProfile()
        .then((res) => {
          if (res.success && res.data) {
            const allowedRoles = ['super_admin', 'admin', 'support'];
            if (!allowedRoles.includes(res.data.role)) {
              sessionStorage.removeItem('authToken');
              localStorage.removeItem('authToken');
              setToken(null);
              return;
            }
            setUser(res.data);
          }
        })
        .catch(() => {
          sessionStorage.removeItem('authToken');
          localStorage.removeItem('authToken');
        })
        .finally(() => setIsLoading(false));
    } else {
      setIsLoading(false);
    }
  }, []);

  const login = async (newToken: string, rememberMe: boolean) => {
    if (rememberMe) {
      localStorage.setItem('authToken', newToken);
    } else {
      sessionStorage.setItem('authToken', newToken);
    }
    setToken(newToken);

    const res = await getProfile();
    if (res.success && res.data) {
      const allowedRoles = ['super_admin', 'admin', 'support'];
      if (!allowedRoles.includes(res.data.role)) {
        sessionStorage.removeItem('authToken');
        localStorage.removeItem('authToken');
        throw new Error('Access denied. Admin, support, or super admin role required.');
      }
      setUser(res.data);
    }
  };

  const logout = () => {
    sessionStorage.removeItem('authToken');
    localStorage.removeItem('authToken');
    sessionStorage.removeItem('user');
    setUser(null);
    setToken(null);
  };

  return (
    <AuthContext.Provider value={{ user, token, isLoading, login, logout }}>
      {children}
    </AuthContext.Provider>
  );
}
