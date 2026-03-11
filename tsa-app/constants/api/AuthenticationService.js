import { apiClient } from "./apiClient";

export const executeJwtAuthentication=(username,password)=>apiClient.post('/auth/login',{identifier:username,password});
export const register=(payLoad)=>apiClient.post('/auth/register',payLoad);
export const registerAds=(payLoad)=>apiClient.post('/adverts',payLoad);
export const registerCategory=(payLoad)=>apiClient.post('/category',payLoad);
export const getCategories=(type)=>apiClient.get(`/category?type=${type}`);
export const getFeaturedCategories=()=>apiClient.get(`/category?isFeatured=true`);
export const getAdverts=(type)=>apiClient.get(`/adverts?type=${type}`);
export const getPrduct=(id)=>apiClient.get(`/adverts/${id}`);
export const addToCart=(payLoad)=>apiClient.post(`/cart`,payLoad)
export const sendProof=(id,payLoad)=>apiClient.post(`/order/${id}/pay`,payLoad);
export const viewCart=()=>apiClient.get('/cart');
export const getCurrentUser=()=>apiClient.get('/users/me')