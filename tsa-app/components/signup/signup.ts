export interface SignupData {
  // Screen 1: Personal Information
  name: string;
  username: string;
  email: string;
  password: string;
  confirmPassword: string;
  phoneNumber: string;
  country: string;
  state: string;
  city: string;
  address: string;
  profilePhoto: string | null;
  referralCode?: string;

  // Screen 2: Identity Verification Documents
  driversLicenseFront?: string;
  driversLicenseBack?: string;
  ninFront?: string;
  ninBack?: string;
  passportPhoto?: string;
  pvcCard?: string;
  bvn?: string;

  // Screen 3: Facial Verification
  faceFront?: string;
  faceLeft?: string;
  faceRight?: string;
  faceUp?: string;
  faceDown?: string;
}

// Optional: You can also create type definitions for different steps
export interface PersonalInfoStep {
  name: string;
  username: string;
  email: string;
  password: string;
  confirmPassword: string;
  phoneNumber: string;
  country: string;
  state: string;
  city: string;
  address: string;
  profilePhoto: string | null;
  referralCode?: string;
}

export interface IdentityVerificationStep {
  driversLicenseFront?: string;
  driversLicenseBack?: string;
  ninFront?: string;
  ninBack?: string;
  passportPhoto?: string;
  pvcCard?: string;
  bvn?: string;
}

export interface FacialVerificationStep {
  faceFront?: string;
  faceLeft?: string;
  faceRight?: string;
  faceUp?: string;
  faceDown?: string;
}

// Utility types for form validation
export type FormField = keyof SignupData;

export interface ValidationError {
  field: FormField;
  message: string;
}

export interface ValidationResult {
  isValid: boolean;
  errors: ValidationError[];
}

// API response types
export interface ApiResponse<T = any> {
  success: boolean;
  message?: string;
  data?: T;
  errors?: ValidationError[];
}

export interface SignupResponseData {
  userId: string;
  token: string;
  nextStep: 'identity_verification' | 'facial_verification' | 'complete';
}

export interface UploadResponseData {
  url: string;
  publicId: string;
  format: string;
  size: number;
  width?: number;
  height?: number;
}

// Document types for clarity
export type DocumentType = 
  | 'drivers_license_front'
  | 'drivers_license_back'
  | 'nin_front'
  | 'nin_back'
  | 'passport'
  | 'pvc'
  | 'bvn';

export type FacialAngle = 
  | 'faceFront'
  | 'faceLeft'
  | 'faceRight'
  | 'faceUp'
  | 'faceDown';

// Country/State/City types
export interface Country {
  name: string;
  iso2?: string;
  iso3?: string;
  states?: State[];
}

export interface State {
  name: string;
  code?: string;
  state_code?: string;
  cities?: City[];
}

export interface City {
  name: string;
  latitude?: number;
  longitude?: number;
}

// Verification status types
export type VerificationStatus = 
  | 'pending'
  | 'in_review'
  | 'verified'
  | 'rejected';

export interface VerificationStatusData {
  overall: VerificationStatus;
  documents: {
    driversLicense: boolean;
    nin: boolean;
    passport: boolean;
    pvc: boolean;
    bvn: boolean;
  };
  facial: {
    verified: boolean;
    score?: number;
    verifiedAt?: Date;
  };
  submittedAt?: Date;
  notes?: string;
}

// User profile type (after signup)
export interface UserProfile {
  _id: string;
  name: string;
  username: string;
  email: string;
  phoneNumber: string;
  country: string;
  state?: string;
  city?: string;
  address: string;
  profilePhoto?: {
    url: string;
    publicId: string;
  };
  verificationStatus: VerificationStatus;
  accountStatus: 'active' | 'inactive' | 'suspended' | 'deleted';
  createdAt: Date;
  updatedAt: Date;
}

// Helper functions
export const createEmptySignupData = (): SignupData => ({
  name: '',
  username: '',
  email: '',
  password: '',
  confirmPassword: '',
  phoneNumber: '',
  country: '',
  state: '',
  city: '',
  address: '',
  profilePhoto: null,
  referralCode: '',
});

export const validatePersonalInfo = (data: PersonalInfoStep): ValidationResult => {
  const errors: ValidationError[] = [];

  // Required fields validation
  const requiredFields: (keyof PersonalInfoStep)[] = [
    'name', 'username', 'email', 'password', 'confirmPassword',
    'phoneNumber', 'country', 'address'
  ];

  requiredFields.forEach(field => {
    if (!data[field] || data[field].toString().trim() === '') {
      errors.push({
        field: field as FormField,
        message: `${field.charAt(0).toUpperCase() + field.slice(1)} is required`
      });
    }
  });

  // Email validation
  const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
  if (data.email && !emailRegex.test(data.email)) {
    errors.push({
      field: 'email',
      message: 'Please enter a valid email address'
    });
  }

  // Password validation
  if (data.password) {
    if (data.password.length < 8) {
      errors.push({
        field: 'password',
        message: 'Password must be at least 8 characters long'
      });
    }

    if (!/[A-Z]/.test(data.password)) {
      errors.push({
        field: 'password',
        message: 'Password must contain at least one uppercase letter'
      });
    }

    if (!/[a-z]/.test(data.password)) {
      errors.push({
        field: 'password',
        message: 'Password must contain at least one lowercase letter'
      });
    }

    if (!/\d/.test(data.password)) {
      errors.push({
        field: 'password',
        message: 'Password must contain at least one number'
      });
    }
  }

  // Password confirmation
  if (data.password !== data.confirmPassword) {
    errors.push({
      field: 'confirmPassword',
      message: 'Passwords do not match'
    });
  }

  return {
    isValid: errors.length === 0,
    errors
  };
};

export const validateIdentityDocuments = (data: IdentityVerificationStep): ValidationResult => {
  const errors: ValidationError[] = [];

  // At least one document should be provided (excluding BVN)
  const hasDocument = 
    data.driversLicenseFront || 
    data.driversLicenseBack || 
    data.ninFront || 
    data.ninBack || 
    data.passportPhoto || 
    data.pvcCard;

  if (!hasDocument) {
    errors.push({
      field: 'driversLicenseFront', // Using first document field as reference
      message: 'Please upload at least one ID document'
    });
  }

  // BVN validation if provided
  if (data.bvn && !/^\d{11}$/.test(data.bvn)) {
    errors.push({
      field: 'bvn',
      message: 'BVN must be 11 digits'
    });
  }

  return {
    isValid: errors.length === 0,
    errors
  };
};

export const validateFacialVerification = (data: FacialVerificationStep): ValidationResult => {
  const errors: ValidationError[] = [];

  const requiredAngles: FacialAngle[] = [
    'faceFront', 'faceLeft', 'faceRight', 'faceUp', 'faceDown'
  ];

  const missingAngles = requiredAngles.filter(angle => !data[angle]);

  if (missingAngles.length > 0) {
    errors.push({
      field: 'faceFront',
      message: `Please capture all facial angles. Missing: ${missingAngles.length} angle(s)`
    });
  }

  return {
    isValid: errors.length === 0,
    errors
  };
};

// Constants for the signup flow
export const SIGNUP_STEPS = {
  PERSONAL_INFO: 1,
  IDENTITY_VERIFICATION: 2,
  FACIAL_VERIFICATION: 3,
} as const;

export const MAX_FILE_SIZE = 5 * 1024 * 1024; // 5MB
export const ALLOWED_IMAGE_TYPES = ['image/jpeg', 'image/jpg', 'image/png', 'image/gif'];

// Export everything
export default SignupData;