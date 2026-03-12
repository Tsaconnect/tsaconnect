export interface SignupData {
  name: string;
  username: string;
  email: string;
  phoneNumber: string;
  password: string;
  confirmPassword: string;
  address: string;
  referralCode?: string;
}

export const createEmptySignupData = (): SignupData => ({
  name: '',
  username: '',
  email: '',
  phoneNumber: '',
  password: '',
  confirmPassword: '',
  address: '',
  referralCode: '',
});
