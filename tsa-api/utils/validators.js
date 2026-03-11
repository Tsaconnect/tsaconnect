// utils/validators.js - Custom validators
const User = require('../models/User');

const validators = {
  // Check if username is available
  isUsernameAvailable: async (username) => {
    const user = await User.findOne({ username: username.toLowerCase() });
    return !user;
  },

  // Check if email is available
  isEmailAvailable: async (email) => {
    const user = await User.findOne({ email: email.toLowerCase() });
    return !user;
  },

  // Check if phone number is available
  isPhoneAvailable: async (phoneNumber) => {
    const user = await User.findOne({ phoneNumber });
    return !user;
  },

  // Validate referral code
  isValidReferralCode: async (referralCode) => {
    const user = await User.findOne({ 
      referralCode,
      accountStatus: 'active' 
    });
    return !!user;
  },

  // Validate BVN format (Nigeria specific)
  isValidBVN: (bvn) => {
    return /^\d{11}$/.test(bvn);
  },

  // Validate phone number
  isValidPhoneNumber: (phoneNumber) => {
    // Basic validation - can be enhanced with libphonenumber
    return /^\+?[\d\s\-\(\)]{10,}$/.test(phoneNumber);
  },

  // Password strength validator
  isStrongPassword: (password) => {
    const minLength = 8;
    const hasUpperCase = /[A-Z]/.test(password);
    const hasLowerCase = /[a-z]/.test(password);
    const hasNumbers = /\d/.test(password);
    const hasSpecialChar = /[!@#$%^&*(),.?":{}|<>]/.test(password);

    return {
      isValid: password.length >= minLength && hasUpperCase && hasLowerCase && hasNumbers,
      details: {
        length: password.length >= minLength,
        upperCase: hasUpperCase,
        lowerCase: hasLowerCase,
        numbers: hasNumbers,
        specialChar: hasSpecialChar
      }
    };
  }
};

module.exports = validators;