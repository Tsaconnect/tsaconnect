import axios from 'axios';
import { toast } from 'sonner';

const API_BASE_URL = import.meta.env.VITE_API_BASE_URL || '/api';

const client = axios.create({
  baseURL: API_BASE_URL,
  headers: { 'Content-Type': 'application/json' },
});

client.interceptors.request.use((config) => {
  const token =
    sessionStorage.getItem('authToken') ||
    localStorage.getItem('authToken');
  if (token) {
    config.headers.Authorization = `Bearer ${token}`;
  }
  return config;
});

client.interceptors.response.use(
  (response) => response,
  (error) => {
    if (!error.response) {
      toast.error('Network error. Check your connection.');
      return Promise.reject(error);
    }

    const { status, data } = error.response;

    switch (status) {
      case 401:
        sessionStorage.removeItem('authToken');
        localStorage.removeItem('authToken');
        sessionStorage.removeItem('user');
        window.location.href = '/login';
        break;
      case 403:
        toast.error("You don't have permission to perform this action");
        break;
      case 422:
        if (!data?.errors) {
          toast.error(data?.message || 'Validation error');
        }
        break;
      case 429:
        toast.error('Too many requests. Please wait.');
        break;
      default:
        if (status >= 500) {
          toast.error('Something went wrong. Please try again.');
        }
    }

    return Promise.reject(error);
  }
);

export default client;
