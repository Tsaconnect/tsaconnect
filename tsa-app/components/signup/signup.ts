export interface SignupData {
  name: string;
  username: string;
  email: string;
  password: string;
  confirmPassword: string;
  referralCode?: string;
}

export const createEmptySignupData = (): SignupData => ({
  name: '',
  username: '',
  email: '',
  password: '',
  confirmPassword: '',
  referralCode: '',
});
