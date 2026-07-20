import {
  countUsernames,
  MIN_ONBOARDING_USERNAMES,
  validateOnboardingForm,
  validateSignupForm,
  type OnboardingFormFields,
  type SignupFormFields,
} from '../rewardForms';

const signup = (over: Partial<SignupFormFields> = {}): SignupFormFields => ({
  name: 'Ada',
  username: 'ada',
  phoneNumber: '08030000000',
  phoneNetwork: 'mtn',
  inviterUsername: '',
  socialLinks: '',
  ...over,
});

const tenNames = Array.from({ length: 10 }, (_, i) => `user${i}`).join(',');

const onboarding = (over: Partial<OnboardingFormFields> = {}): OnboardingFormFields => ({
  name: 'Ada',
  username: 'ada',
  payoutMethod: 'bank',
  payoutDetails: '0123456789 GTB',
  invitedUsernames: tenNames,
  ...over,
});

describe('countUsernames', () => {
  it('counts non-blank entries across separators', () => {
    expect(countUsernames('a,b,c')).toBe(3);
    expect(countUsernames('a, b, c,')).toBe(3); // trailing comma
    expect(countUsernames('a\nb\nc')).toBe(3);
    expect(countUsernames('  a   b  ')).toBe(2);
    expect(countUsernames('')).toBe(0);
    expect(countUsernames(',,,')).toBe(0);
  });
});

describe('validateSignupForm', () => {
  it('accepts a complete form', () => {
    expect(validateSignupForm(signup())).toEqual({});
  });

  it('requires name, username, phone and network', () => {
    const errors = validateSignupForm(
      signup({ name: ' ', username: '', phoneNumber: '', phoneNetwork: '' }),
    );
    expect(Object.keys(errors).sort()).toEqual(['name', 'phoneNetwork', 'phoneNumber', 'username']);
  });

  it('rejects an unsupported network', () => {
    expect(validateSignupForm(signup({ phoneNetwork: 'vodafone' })).phoneNetwork).toBeTruthy();
  });

  it('accepts local and country-code phone formats, rejects junk lengths', () => {
    expect(validateSignupForm(signup({ phoneNumber: '08030000000' })).phoneNumber).toBeUndefined();
    expect(validateSignupForm(signup({ phoneNumber: '+2348030000000' })).phoneNumber).toBeUndefined();
    expect(validateSignupForm(signup({ phoneNumber: '0803 000 0000' })).phoneNumber).toBeUndefined();
    expect(validateSignupForm(signup({ phoneNumber: '123' })).phoneNumber).toBeTruthy();
  });

  it('does not require the optional inviter or socials', () => {
    expect(validateSignupForm(signup({ inviterUsername: '', socialLinks: '' }))).toEqual({});
  });
});

describe('validateOnboardingForm', () => {
  it('accepts a complete form with the minimum usernames', () => {
    expect(validateOnboardingForm(onboarding())).toEqual({});
  });

  it('requires payout method and details', () => {
    const errors = validateOnboardingForm(onboarding({ payoutMethod: '', payoutDetails: '  ' }));
    expect(errors.payoutMethod).toBeTruthy();
    expect(errors.payoutDetails).toBeTruthy();
  });

  it('enforces the minimum username count and reports the shortfall', () => {
    const errors = validateOnboardingForm(onboarding({ invitedUsernames: 'a,b,c' }));
    expect(errors.invitedUsernames).toContain(String(MIN_ONBOARDING_USERNAMES));
    expect(errors.invitedUsernames).toContain('you have 3');
  });

  it('treats the minimum as a floor, not an exact count', () => {
    const eleven = `${tenNames},extra`;
    expect(validateOnboardingForm(onboarding({ invitedUsernames: eleven }))).toEqual({});
  });
});
