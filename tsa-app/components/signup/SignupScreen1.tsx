import React, { useState } from 'react';
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  ActivityIndicator,
} from 'react-native';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS } from '../../constants/theme';
import { SignupData } from './signup';
import { router } from 'expo-router';

interface SignupScreen1Props {
  data: SignupData;
  updateData: (data: Partial<SignupData>) => void;
  onNext: () => void;
  onBack: () => void;
  isLoading?: boolean;
}

type FieldErrors = {
  name?: string;
  username?: string;
  email?: string;
  phoneNumber?: string;
  address?: string;
  password?: string;
  confirmPassword?: string;
};

const SignupScreen1: React.FC<SignupScreen1Props> = ({
  data,
  updateData,
  onNext,
  onBack,
  isLoading = false,
}) => {
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [errors, setErrors] = useState<FieldErrors>({});

  const clearError = (field: keyof FieldErrors) => {
    if (errors[field]) {
      setErrors(prev => ({ ...prev, [field]: undefined }));
    }
  };

  const validateFields = (): boolean => {
    const newErrors: FieldErrors = {};

    if (!data.name.trim()) {
      newErrors.name = 'Full name is required';
    }
    if (!data.username.trim()) {
      newErrors.username = 'Username is required';
    }
    if (!data.email.trim()) {
      newErrors.email = 'Email is required';
    } else if (!/^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(data.email)) {
      newErrors.email = 'Enter a valid email address';
    }
    if (!data.phoneNumber.trim()) {
      newErrors.phoneNumber = 'Phone number is required';
    }
    if (!data.address.trim()) {
      newErrors.address = 'Address is required';
    }
    if (!data.password) {
      newErrors.password = 'Password is required';
    } else if (data.password.length < 8) {
      newErrors.password = 'Must be at least 8 characters';
    } else if (!(/[A-Z]/.test(data.password) && /[a-z]/.test(data.password) && /\d/.test(data.password))) {
      newErrors.password = 'Include uppercase, lowercase, and a number';
    }
    if (!data.confirmPassword) {
      newErrors.confirmPassword = 'Please confirm your password';
    } else if (data.password !== data.confirmPassword) {
      newErrors.confirmPassword = 'Passwords do not match';
    }

    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleNext = () => {
    if (validateFields()) {
      onNext();
    }
  };

  const renderInput = (
    icon: string,
    placeholder: string,
    field: keyof SignupData,
    options?: {
      keyboardType?: 'default' | 'email-address' | 'phone-pad';
      autoCapitalize?: 'none' | 'words';
      secureTextEntry?: boolean;
      toggleVisibility?: () => void;
      isVisible?: boolean;
    }
  ) => {
    const hasError = !!errors[field as keyof FieldErrors];
    return (
      <View style={styles.fieldWrapper} key={field}>
        <View style={[styles.inputContainer, hasError && styles.inputError]}>
          <MaterialIcons name={icon as any} size={20} color={hasError ? COLORS.danger : COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder={placeholder}
            placeholderTextColor="#999"
            value={data[field] || ''}
            onChangeText={(text) => {
              updateData({ [field]: text });
              clearError(field as keyof FieldErrors);
            }}
            keyboardType={options?.keyboardType || 'default'}
            autoCapitalize={options?.autoCapitalize ?? 'none'}
            secureTextEntry={options?.secureTextEntry}
            editable={!isLoading}
          />
          {options?.toggleVisibility && (
            <TouchableOpacity
              onPress={options.toggleVisibility}
              style={styles.passwordIcon}
              disabled={isLoading}
            >
              <MaterialIcons
                name={options.isVisible ? 'visibility' : 'visibility-off'}
                size={20}
                color={COLORS.gray}
              />
            </TouchableOpacity>
          )}
        </View>
        {hasError && (
          <Text style={styles.errorText}>{errors[field as keyof FieldErrors]}</Text>
        )}
      </View>
    );
  };

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      <View style={styles.header}>
        <TouchableOpacity onPress={onBack} style={styles.backButton}>
          <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
        </TouchableOpacity>
        <Text style={styles.title}>Create Account</Text>
      </View>

      {renderInput('person', 'Full Name', 'name', { autoCapitalize: 'words' })}
      {renderInput('alternate-email', 'Username', 'username')}
      {renderInput('email', 'Email Address', 'email', { keyboardType: 'email-address' })}
      {renderInput('phone', 'Phone Number', 'phoneNumber', { keyboardType: 'phone-pad' })}
      {renderInput('home', 'Address', 'address')}
      {renderInput('lock', 'Password (min. 8 characters)', 'password', {
        secureTextEntry: !showPassword,
        toggleVisibility: () => setShowPassword(!showPassword),
        isVisible: showPassword,
      })}
      {renderInput('lock-outline', 'Confirm Password', 'confirmPassword', {
        secureTextEntry: !showConfirmPassword,
        toggleVisibility: () => setShowConfirmPassword(!showConfirmPassword),
        isVisible: showConfirmPassword,
      })}
      {renderInput('card-giftcard', 'Referral Code (Optional)', 'referralCode')}

      <TouchableOpacity
        style={[styles.submitButton, isLoading && styles.submitButtonDisabled]}
        onPress={handleNext}
        disabled={isLoading}
      >
        {isLoading ? (
          <ActivityIndicator size="small" color="#fff" />
        ) : (
          <Text style={styles.submitButtonText}>Create Account</Text>
        )}
      </TouchableOpacity>

      <View style={styles.footer}>
        <Text style={styles.footerText}>
          Already have an account?{' '}
        </Text>
        <TouchableOpacity onPress={() => router.push('/login')} disabled={isLoading}>
          <Text style={styles.loginLink}>Login</Text>
        </TouchableOpacity>
      </View>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    padding: 20,
    backgroundColor: '#fff',
  },
  header: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 30,
  },
  backButton: {
    marginRight: 15,
  },
  title: {
    fontSize: 28,
    fontWeight: '700',
    color: COLORS.primary,
  },
  fieldWrapper: {
    marginBottom: 4,
  },
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    paddingHorizontal: 15,
    marginBottom: 2,
  },
  inputError: {
    borderColor: COLORS.danger,
    borderWidth: 1.5,
    backgroundColor: '#fff5f5',
  },
  inputIcon: {
    marginRight: 10,
  },
  input: {
    flex: 1,
    height: 56,
    fontSize: 16,
    color: COLORS.dark,
  },
  passwordIcon: {
    padding: 10,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 12,
    marginLeft: 15,
    marginBottom: 8,
  },
  submitButton: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    height: 56,
    justifyContent: 'center',
    alignItems: 'center',
    marginTop: 10,
    marginBottom: 30,
    shadowColor: COLORS.primary,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 5,
  },
  submitButtonDisabled: {
    backgroundColor: COLORS.lightGray,
    opacity: 0.7,
  },
  submitButtonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
  },
  footer: {
    flexDirection: 'row',
    justifyContent: 'center',
    alignItems: 'center',
    marginBottom: 30,
  },
  footerText: {
    color: COLORS.gray,
    fontSize: 14,
  },
  loginLink: {
    color: COLORS.primary,
    fontWeight: '600',
    fontSize: 14,
  },
});

export default SignupScreen1;
