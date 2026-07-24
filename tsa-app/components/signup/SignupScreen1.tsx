// components/signup/SignupScreen1.tsx
//
// Signup step 1 — collects the user's account fields with inline validation.
// Restyled onto the TSA Signature design system (tokens + @/components/ui
// primitives only). Props interface, exports, field validation, the updateData
// wiring, the showPassword/showConfirmPassword toggles, and the /login navigation
// are preserved verbatim — this is a visual/structural migration only.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button,
  Icon,
  IconButton,
  IconName,
  Input,
  PressableScale,
  Text,
} from '@/components/ui';
import { SignupData } from './signup';

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
  const { colors, spacing } = useTheme();
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
    icon: IconName,
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
    const error = errors[field as keyof FieldErrors];
    const iconColor = error ? colors.danger : colors.textMuted;
    return (
      <Input
        key={field}
        containerStyle={{ marginBottom: spacing.md }}
        placeholder={placeholder}
        value={data[field] || ''}
        onChangeText={(text) => {
          updateData({ [field]: text });
          clearError(field as keyof FieldErrors);
        }}
        keyboardType={options?.keyboardType || 'default'}
        autoCapitalize={options?.autoCapitalize ?? 'none'}
        secureTextEntry={options?.secureTextEntry}
        editable={!isLoading}
        error={error}
        leftAdornment={<Icon name={icon} size={20} color={iconColor} />}
        rightAdornment={
          options?.toggleVisibility ? (
            <IconButton
              icon={options.isVisible ? 'eye' : 'eyeOff'}
              size={20}
              color={colors.textMuted}
              onPress={options.toggleVisibility}
              disabled={isLoading}
              accessibilityLabel={options.isVisible ? 'Hide password' : 'Show password'}
              style={{ width: 32, height: 32 }}
            />
          ) : undefined
        }
      />
    );
  };

  return (
    <ScrollView
      style={{ flex: 1 }}
      contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
      showsVerticalScrollIndicator={false}
      keyboardShouldPersistTaps="handled"
    >
      <View
        style={{
          flexDirection: 'row',
          alignItems: 'center',
          gap: spacing.sm,
          marginBottom: spacing['2xl'],
        }}
      >
        <IconButton
          icon="chevronLeft"
          onPress={onBack}
          color={colors.accent}
          accessibilityLabel="Go back"
        />
        <Text variant="title1">Create Account</Text>
      </View>

      {renderInput('user', 'Full Name', 'name', { autoCapitalize: 'words' })}
      {renderInput('at', 'Username', 'username')}
      {renderInput('mail', 'Email Address', 'email', { keyboardType: 'email-address' })}
      {renderInput('mobile', 'Phone Number', 'phoneNumber', { keyboardType: 'phone-pad' })}
      {renderInput('home', 'Address', 'address')}
      {renderInput('lock', 'Password (min. 8 characters)', 'password', {
        secureTextEntry: !showPassword,
        toggleVisibility: () => setShowPassword(!showPassword),
        isVisible: showPassword,
      })}
      {renderInput('shield', 'Confirm Password', 'confirmPassword', {
        secureTextEntry: !showConfirmPassword,
        toggleVisibility: () => setShowConfirmPassword(!showConfirmPassword),
        isVisible: showConfirmPassword,
      })}
      {renderInput('gift', 'Referral Code (Optional)', 'referralCode')}

      <Button
        title="Create Account"
        onPress={handleNext}
        loading={isLoading}
        disabled={isLoading}
        style={{ marginTop: spacing.sm }}
      />

      {/* Consent-by-action notice. Both stores require the terms and privacy
          policy to be reachable before an account is created, not only after. */}
      <View style={{ marginTop: spacing.md, marginBottom: spacing['2xl'] }}>
        <Text variant="caption" color="textMuted" align="center">
          By creating an account you agree to our{' '}
          <Text
            variant="caption"
            weight="600"
            color="accent"
            onPress={() => router.push('/legal/terms')}
          >
            Terms of Service
          </Text>{' '}
          and{' '}
          <Text
            variant="caption"
            weight="600"
            color="accent"
            onPress={() => router.push('/legal/privacy')}
          >
            Privacy Policy
          </Text>
          .
        </Text>
      </View>

      <View
        style={{
          flexDirection: 'row',
          justifyContent: 'center',
          alignItems: 'center',
          marginBottom: spacing['2xl'],
        }}
      >
        <Text variant="bodySm" color="textSecondary">
          Already have an account?{' '}
        </Text>
        <PressableScale onPress={() => router.push('/login')} disabled={isLoading}>
          <Text variant="bodySm" weight="600" color="accent">
            Login
          </Text>
        </PressableScale>
      </View>
    </ScrollView>
  );
};

export default SignupScreen1;
