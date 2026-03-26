import React, {
  createContext,
  useContext,
  useEffect,
  useState,
  ReactNode,
} from "react";
import AsyncStorage from "@react-native-async-storage/async-storage";
import {
  addToCart,
  executeJwtAuthentication,
  getCurrentUser,
  register,
} from "../constants/api/AuthenticationService";
import { apiClient } from "../constants/api/apiClient";
import { router } from "expo-router";
import * as FileSystem from 'expo-file-system';

interface AppContextType {
  category: string;
  setCategory: React.Dispatch<React.SetStateAction<string>>;
  isAuthenticated: boolean;
  setAuthenticated: React.Dispatch<React.SetStateAction<boolean>>;
  login: (
    username: string,
    password: string
  ) => Promise<{ success: boolean; error: boolean; message: string }>;
  logOut: () => Promise<void>;
  username: string;
  token: string;
  loading: boolean;
  setLoading: React.Dispatch<React.SetStateAction<boolean>>;
  appService: string;
  setAppService: React.Dispatch<React.SetStateAction<string>>;
  signup: (
    payLoad: any
  ) => Promise<{ success: boolean; error: boolean; message: string }>;
  setToken: React.Dispatch<React.SetStateAction<string>>;
  items: any[];
  setItems: React.Dispatch<React.SetStateAction<any[]>>;
  addItem: (
    newItem: any
  ) => Promise<{ success: boolean; error: boolean; message: string }>;
  removeItem: (id: string) => void;
  getItems: () => Promise<{ success: boolean; data: any; message: string }>;
  currentUser: any;
  tTy: any;
  settTy: React.Dispatch<React.SetStateAction<any>>;
  setCurrentUser: React.Dispatch<React.SetStateAction<any>>;
  emailVerified: boolean;
  setEmailVerified: React.Dispatch<React.SetStateAction<boolean>>;
}

const defaultContextValue: AppContextType = {
  category: "",
  setCategory: () => {},
  isAuthenticated: false,
  setAuthenticated: () => {},
  login: async () => ({ success: false, error: true, message: "" }),
  logOut: async () => {},
  username: "",
  token: "",
  loading: false,
  setLoading: () => {},
  appService: "",
  setAppService: () => {},
  signup: async () => ({ success: false, error: true, message: "" }),
  setToken: () => {},
  items: [],
  setItems: () => {},
  addItem: async () => ({ success: false, error: true, message: "" }),
  removeItem: () => {},
  getItems: async () => ({ success: false, data: "", message: "" }),
  currentUser: null,
  tTy: null,
  settTy: () => {},
  setCurrentUser: () => {},
  emailVerified: false,
  setEmailVerified: () => {},
};

export const AppContext = createContext<AppContextType>(defaultContextValue);
export const useAuth = () => useContext(AppContext);

interface AppProviderProps {
  children: ReactNode;
}

export const AppProvider: React.FC<AppProviderProps> = ({ children }) => {
  const [category, setCategory] = useState<string>("");
  const [isAuthenticated, setAuthenticated] = useState<boolean>(false);
  const [username, setUsername] = useState<string>("");
  const [token, setToken] = useState<string>("");
  const [loading, setLoading] = useState<boolean>(false);
  const [appService, setAppService] = useState<string>("");
  const [items, setItems] = useState<any[]>([]);
  const [currentUser, setCurrentUser] = useState<any>();
  const [tTy, settTy] = useState<any>();
  const [emailVerified, setEmailVerified] = useState<boolean>(false);

  const addItem = async (newItem: any) => {
    try {
      const response = await addToCart(newItem);
      if (response.status === 201) {
        return { success: true, error: false, message: "success" };
      } else {
        return { success: false, error: true, message: "could not create" };
      }
    } catch (error: any) {
      return {
        success: false,
        error: true,
        message: error.response?.data?.message,
      };
    }
  };

  const removeItem = (id: string) => {
    setItems((prevItems) => prevItems.filter((item) => item.id !== id));
  };

  async function login(username: string, password: string) {
    try {
      const response = await executeJwtAuthentication(username, password);
      if (response.status === 200) {
        const jwtToken = "Bearer " + response.data.tokens.access.token;
        await AsyncStorage.setItem("token", jwtToken);
        await AsyncStorage.setItem("authToken", jwtToken);
        setToken(jwtToken);
        setCurrentUser(response.data.user);
        setUsername(username);
        setAuthenticated(true);
        apiClient.interceptors.request.use((config: any) => {
          config.headers.Authorization = jwtToken;
          config.headers["Content-Type"] = "multipart/form-data";
          return config;
        });
        return { success: true, error: false, message: "success" };
      } else {
        setLoading(false);
        await logOut();
        return { success: false, error: true, message: "could not create" };
      }
    } catch (error: any) {
      setLoading(false);
      await logOut();
      return {
        success: false,
        error: true,
        message: error.response?.data?.message,
      };
    }
  }

  const getItems = async () => {
    try {
      const token = await AsyncStorage.getItem("token");
      const response = await apiClient.get(`/cart`, {
        headers: {
          Authorization: token || "",
        },
      });
      if (response.status === 200) {
        return { success: true, data: response.data, message: "success" };
      } else {
        return { success: false, data: "", message: "failed to getItems" };
      }
    } catch (error: any) {
      return {
        success: false,
        data: "",
        message: error.response?.data?.message,
      };
    }
  };


  // async function signup(payLoad: any) {
  //   try {
  //     const response = await register(payLoad);
  //     if (response.status === 201) {
  //       const jwtToken = "Bearer " + response.data.tokens.access.token;
  //       setToken(jwtToken);
  //       setAuthenticated(true);
  //       apiClient.interceptors.request.use((config: any) => {
  //         config.headers.Authorization = jwtToken;
  //         config.headers["Content-Type"] = "multipart/form-data";
  //         return config;
  //       });
  //       return { success: true, error: false, message: "success" };
  //     } else {
  //       setLoading(false);
  //       return { success: false, error: true, message: "could not create" };
  //     }
  //   } catch (error: any) {
  //     setLoading(false);
  //     return {
  //       success: false,
  //       error: true,
  //       message: error.response?.data?.message,
  //     };
  //   } finally {
  //     setLoading(false);
  //   }
  // }
// AuthContext/AuthContext.tsx

// Define proper types
interface SignupPayload {
  // Personal Information
  name: string;
  username: string;
  email: string;
  phoneNumber: string;
  password: string;
  
  // Location
  country: string;
  state: string;
  city: string;
  address: string;
  
  // Files (will be handled as FormData)
  profilePhoto?: string;
  driversLicenseFront?: string;
  driversLicenseBack?: string;
  ninFront?: string;
  ninBack?: string;
  passportPhoto?: string;
  pvcCard?: string;
  
  // Facial Verification
  faceFront?: string;
  faceLeft?: string;
  faceRight?: string;
  faceUp?: string;
  faceDown?: string;
  
  // Optional
  referralCode?: string;
  bvn?: string;
}

// Enhanced signup function
async function signup(payload: SignupPayload) {
  try {
    setLoading(true);
    
    // Create FormData for multipart upload
    const formData = new FormData();
    
    // Append text fields
    Object.entries(payload).forEach(([key, value]) => {
      if (key !== 'profilePhoto' && 
          key !== 'driversLicenseFront' && 
          key !== 'driversLicenseBack' && 
          key !== 'ninFront' && 
          key !== 'ninBack' && 
          key !== 'passportPhoto' && 
          key !== 'pvcCard' && 
          key !== 'faceFront' && 
          key !== 'faceLeft' && 
          key !== 'faceRight' && 
          key !== 'faceUp' && 
          key !== 'faceDown' &&
          value !== undefined) {
        formData.append(key, value as string);
      }
    });
    
    // Helper function to append files
    const appendFile = async (fieldName: string, uri: string | undefined, fileName: string) => {
      if (uri && uri.startsWith('file://')) {
        try {
          const fileInfo = await FileSystem.getInfoAsync(uri);
          if (fileInfo.exists) {
            const file = {
              uri,
              name: fileName,
              type: 'image/jpeg',
            } as any;
            formData.append(fieldName, file);
          }
        } catch (error) {
          console.warn(`Failed to process file for ${fieldName}:`, error);
        }
      }
    };
    
    // Append all files
    await appendFile('profilePhoto', payload.profilePhoto, 'profile.jpg');
    await appendFile('driversLicenseFront', payload.driversLicenseFront, 'drivers_license_front.jpg');
    await appendFile('driversLicenseBack', payload.driversLicenseBack, 'drivers_license_back.jpg');
    await appendFile('ninFront', payload.ninFront, 'nin_front.jpg');
    await appendFile('ninBack', payload.ninBack, 'nin_back.jpg');
    await appendFile('passportPhoto', payload.passportPhoto, 'passport.jpg');
    await appendFile('pvcCard', payload.pvcCard, 'pvc_card.jpg');
    await appendFile('faceFront', payload.faceFront, 'face_front.jpg');
    await appendFile('faceLeft', payload.faceLeft, 'face_left.jpg');
    await appendFile('faceRight', payload.faceRight, 'face_right.jpg');
    await appendFile('faceUp', payload.faceUp, 'face_up.jpg');
    await appendFile('faceDown', payload.faceDown, 'face_down.jpg');
    
    // Log payload for debugging (remove in production)
    console.log('Signup payload keys:', Object.keys(payload));
    //console.log('FormData entries count:', formData.getAllKeys().length);
    
    // Make API call with FormData
    const response = await register(formData);
    
    if (response.status === 201) {
      const jwtToken = "Bearer " + response.data.tokens.access.token;
      
      // Save token and authentication state
      await AsyncStorage.setItem('token', jwtToken);
      await AsyncStorage.setItem('authToken', jwtToken);
      setToken(jwtToken);
      setAuthenticated(true);
      
      // Configure API client interceptor
      apiClient.interceptors.request.use((config: any) => {
        config.headers.Authorization = jwtToken;
        // Don't set Content-Type here - let axios set it for multipart
        if (config.data instanceof FormData) {
          config.headers['Content-Type'] = 'multipart/form-data';
        } else {
          config.headers['Content-Type'] = 'application/json';
        }
        return config;
      });
      
      // Save user data to AsyncStorage
      const userData = {
        ...response.data.user,
        token: jwtToken,
      };
      await AsyncStorage.setItem('userData', JSON.stringify(userData));
      
      return { 
        success: true, 
        error: false, 
        message: "Registration successful!",
        data: response.data 
      };
    } else {
      setLoading(false);
      return { 
        success: false, 
        error: true, 
        message: "Registration failed. Please try again." 
      };
    }
  } catch (error: any) {
    console.error('Signup error:', error);
    setLoading(false);
    
    // Extract meaningful error message
    let errorMessage = "An error occurred during registration.";
    
    if (error.response) {
      // Server responded with error
      if (error.response.data?.message) {
        errorMessage = error.response.data.message;
      } else if (error.response.data?.errors) {
        // Handle validation errors
        const errors = error.response.data.errors;
        errorMessage = Object.values(errors).flat().join(', ');
      } else if (error.response.status === 400) {
        errorMessage = "Invalid data provided.";
      } else if (error.response.status === 409) {
        errorMessage = "User already exists with this email or phone number.";
      } else if (error.response.status === 500) {
        errorMessage = "Server error. Please try again later.";
      }
    } else if (error.request) {
      // Request made but no response
      errorMessage = "No response from server. Check your internet connection.";
    } else {
      // Something else happened
      errorMessage = error.message || errorMessage;
    }
    
    return {
      success: false,
      error: true,
      message: errorMessage,
    };
  } finally {
    setLoading(false);
  }
}

// Also update your register API function to handle FormData
// api/register.ts
const register = async (formData: FormData) => {
  return apiClient.post('/auth/register', formData, {
    headers: {
      'Content-Type': 'multipart/form-data',
    },
  });
};
  async function logOut() {
    setToken("");
    setAuthenticated(false);
    setUsername("");
    apiClient.interceptors.request.use((config:any) => {
      config.headers.Authorization = "";
      return config;
    });
    await AsyncStorage.removeItem("token");
    await AsyncStorage.removeItem("authToken");
    setCurrentUser(null);
    //@ts-ignore
    router.push("/login");
  }

  async function hydrateAuth() {
    try {
      // Check both storage keys — api.ts uses "authToken", legacy uses "token"
      const storedToken = await AsyncStorage.getItem("authToken") || await AsyncStorage.getItem("token");
      if (storedToken) {
        setToken(storedToken);
        setAuthenticated(true);
        // Sync both keys
        await AsyncStorage.setItem("authToken", storedToken);
        await AsyncStorage.setItem("token", storedToken);
        // Set up axios interceptor
        apiClient.interceptors.request.use((config: any) => {
          config.headers.Authorization = storedToken;
          return config;
        });
        // Fetch user profile
        try {
          const response = await getCurrentUser();
          const userData = response.data?.data ?? response.data;
          setCurrentUser(userData);
          setEmailVerified(userData?.emailVerified ?? false);
        } catch (err) {
          // Token may be expired — don't force logout, let screens handle it
        }
      }
    } catch (err) {
      console.error("Error hydrating auth:", err);
    }
  }

  useEffect(() => {
    hydrateAuth();
  }, []);

  return (
    <AppContext.Provider
      value={{
        category,
        setCategory,
        isAuthenticated,
        setAuthenticated,
        login,
        logOut,
        username,
        token,
        loading,
        setLoading,
        appService,
        setAppService,
        signup,
        setToken,
        items,
        setItems,
        addItem,
        removeItem,
        getItems,
        currentUser,
        tTy,
        settTy,
        setCurrentUser,
        emailVerified,
        setEmailVerified,
      }}
    >
      {children}
    </AppContext.Provider>
  );
};
