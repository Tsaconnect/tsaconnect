// components/signup/SignupScreen1.tsx
import React, { useState, useEffect } from 'react';
import {
  View,
  Text,
  TextInput,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  Image,
  ActivityIndicator,
  Alert,
} from 'react-native';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import * as ImagePicker from 'expo-image-picker';
import { COLORS, SIZES } from '../../constants/theme';
import PhoneNumber from '../country/phoneNumber';
import CustomPickerWithSearch from './dropdown';
import { SignupData } from './signup';
import { router } from 'expo-router';

interface SignupScreen1Props {
  data: SignupData;
  updateData: (data: Partial<SignupData>) => void;
  onNext: () => void;
  onBack: () => void;
  isLoading?: boolean;
}

interface Country {
  name: string;
  iso2?: string;
  states?: State[];
}

interface State {
  name: string;
  code?: string;
  state_code?: string;
}

interface City {
  name: string;
}

const SignupScreen1: React.FC<SignupScreen1Props> = ({
  data,
  updateData,
  onNext,
  onBack,
  isLoading = false,
}) => {
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [selectedCountry, setSelectedCountry] = useState(null);
  
  // State management for countries, states, and cities
  const [countries, setCountries] = useState<Country[]>([]);
  const [states, setStates] = useState<State[]>([]);
  const [cities, setCities] = useState<City[]>([]);
  const [loadingCountries, setLoadingCountries] = useState(false);
  const [loadingStates, setLoadingStates] = useState(false);
  const [loadingCities, setLoadingCities] = useState(false);
  const [error, setError] = useState<string | null>(null);

  // Fetch countries on component mount
  useEffect(() => {
    fetchCountries();
  }, []);

  // Fetch states when country changes
  useEffect(() => {
    if (data.country) {
      fetchStates(data.country);
    } else {
      setStates([]);
      setCities([]);
      updateData({ state: '', city: '' });
    }
  }, [data.country]);

  // Fetch cities when state changes
  useEffect(() => {
    if (data.country && data.state) {
      fetchCities(data.country, data.state);
    } else {
      setCities([]);
      updateData({ city: '' });
    }
  }, [data.state, data.country]);

  // Fetch countries from CountriesNow API
  const fetchCountries = async () => {
    setLoadingCountries(true);
    setError(null);
    
    try {
      const response = await fetch('https://countriesnow.space/api/v0.1/countries/states');
      
      if (!response.ok) {
        throw new Error('Failed to fetch countries');
      }
      
      const result = await response.json();
      
      if (!result.error && Array.isArray(result.data)) {
        const formattedCountries = result.data.map((country: any) => ({
          name: country.name,
          iso2: country.iso2,
          states: country.states?.map((state: any) => ({
            name: state.name,
            code: state.state_code,
            state_code: state.state_code
          })) || []
        })).sort((a:any, b:any) => a.name.localeCompare(b.name));
        
        setCountries(formattedCountries);
      } else {
        throw new Error(result.msg || 'Invalid data format');
      }
    } catch (err: any) {
      console.error('Error fetching countries:', err);
      setError(err.message);
      
      // Fallback to basic country list
      setCountries([
        {
          name: 'Nigeria',
          iso2: 'NG',
          states: [
            { name: 'Lagos', code: 'LOS' },
            { name: 'Abuja', code: 'ABJ' },
            { name: 'Kano', code: 'KAN' },
            { name: 'Rivers', code: 'RIV' },
            { name: 'Oyo', code: 'OYO' },
            { name: 'Kaduna', code: 'KAD' },
            { name: 'Delta', code: 'DEL' },
            { name: 'Ogun', code: 'OGU' },
            { name: 'Enugu', code: 'ENU' },
            { name: 'Plateau', code: 'PLA' }
          ]
        },
        {
          name: 'United States',
          iso2: 'US',
          states: [
            { name: 'California', code: 'CA' },
            { name: 'Texas', code: 'TX' },
            { name: 'New York', code: 'NY' },
            { name: 'Florida', code: 'FL' },
            { name: 'Illinois', code: 'IL' },
            { name: 'Pennsylvania', code: 'PA' },
            { name: 'Ohio', code: 'OH' },
            { name: 'Georgia', code: 'GA' },
            { name: 'North Carolina', code: 'NC' },
            { name: 'Michigan', code: 'MI' }
          ]
        },
        {
          name: 'United Kingdom',
          iso2: 'GB',
          states: [
            { name: 'England', code: 'ENG' },
            { name: 'Scotland', code: 'SCO' },
            { name: 'Wales', code: 'WAL' },
            { name: 'Northern Ireland', code: 'NIR' }
          ]
        },
        {
          name: 'Canada',
          iso2: 'CA',
          states: [
            { name: 'Ontario', code: 'ON' },
            { name: 'Quebec', code: 'QC' },
            { name: 'British Columbia', code: 'BC' },
            { name: 'Alberta', code: 'AB' },
            { name: 'Manitoba', code: 'MB' },
            { name: 'Saskatchewan', code: 'SK' },
            { name: 'Nova Scotia', code: 'NS' },
            { name: 'New Brunswick', code: 'NB' },
            { name: 'Newfoundland and Labrador', code: 'NL' },
            { name: 'Prince Edward Island', code: 'PE' }
          ]
        },
        {
          name: 'Ghana',
          iso2: 'GH',
          states: [
            { name: 'Greater Accra', code: 'AA' },
            { name: 'Ashanti', code: 'AH' },
            { name: 'Western', code: 'WP' },
            { name: 'Eastern', code: 'EP' },
            { name: 'Central', code: 'CP' },
            { name: 'Volta', code: 'TV' },
            { name: 'Northern', code: 'NP' },
            { name: 'Upper East', code: 'UE' },
            { name: 'Upper West', code: 'UW' },
            { name: 'Brong-Ahafo', code: 'BA' }
          ]
        }
      ]);
    } finally {
      setLoadingCountries(false);
    }
  };

  const fetchStates = async (countryName: string) => {
    setLoadingStates(true);
    
    try {
      updateData({ state: '', city: '' });
      setCities([]);
      
      const country = countries.find(c => c.name === countryName);
      if (country?.states && country.states.length > 0) {
        setStates(country.states);
        return;
      }
      
      const response = await fetch('https://countriesnow.space/api/v0.1/countries/states', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ country: countryName }),
      });
      
      if (!response.ok) {
        throw new Error('Failed to fetch states');
      }
      
      const result = await response.json();
      
      if (!result.error && result.data?.states) {
        const statesData = result.data.states.map((state: any) => ({
          name: state.name,
          code: state.state_code,
          state_code: state.state_code
        }));
        
        setStates(statesData);
        
        setCountries(prev => prev.map(c => 
          c.name === countryName 
            ? { ...c, states: statesData }
            : c
        ));
      } else {
        setStates([]);
      }
    } catch (err) {
      console.error('Error fetching states:', err);
      setStates([]);
    } finally {
      setLoadingStates(false);
    }
  };

  const fetchCities = async (countryName: string, stateName: string) => {
    setLoadingCities(true);
    
    try {
      updateData({ city: '' });
      
      const response = await fetch('https://countriesnow.space/api/v0.1/countries/state/cities', {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
        },
        body: JSON.stringify({ 
          country: countryName,
          state: stateName 
        }),
      });
      
      if (!response.ok) {
        throw new Error('Failed to fetch cities');
      }
      
      const result = await response.json();
      
      if (!result.error && result.data) {
        const citiesData = result.data.map((city: any) => ({
          name: city
        }));
        
        setCities(citiesData);
      } else {
        setCities([]);
      }
    } catch (err) {
      console.error('Error fetching cities:', err);
      setCities([]);
    } finally {
      setLoadingCities(false);
    }
  };

  const pickImage = async () => {
    const { status } = await ImagePicker.requestMediaLibraryPermissionsAsync();
    if (status !== 'granted') {
      Alert.alert(
        'Permission Required',
        'Sorry, we need camera roll permissions to make this work!',
        [{ text: 'OK' }]
      );
      return;
    }

    const result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      allowsEditing: true,
      aspect: [1, 1],
      quality: 0.8,
    });

    if (!result.canceled) {
      updateData({ profilePhoto: result.assets[0].uri });
    }
  };

  const validateFields = () => {
    const requiredFields = [
      'name',
      'username',
      'country',
      'address',
      'phoneNumber',
      'email',
      'password',
      'confirmPassword',
    ];

    for (const field of requiredFields) {
      if (!data[field as keyof SignupData]) {
        Alert.alert('Required Field', `Please fill in ${field}`);
        return false;
      }
    }

    if (data.password !== data.confirmPassword) {
      Alert.alert('Password Mismatch', 'Passwords do not match');
      return false;
    }

    const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
    if (!emailRegex.test(data.email)) {
      Alert.alert('Invalid Email', 'Please enter a valid email address');
      return false;
    }

    if (data.password.length < 8) {
      Alert.alert('Weak Password', 'Password must be at least 8 characters long');
      return false;
    }

    const hasUpperCase = /[A-Z]/.test(data.password);
    const hasLowerCase = /[a-z]/.test(data.password);
    const hasNumbers = /\d/.test(data.password);

    if (!(hasUpperCase && hasLowerCase && hasNumbers)) {
      Alert.alert(
        'Weak Password',
        'Password should include uppercase, lowercase letters, and numbers'
      );
      return false;
    }

    return true;
  };

  const handleNext = () => {
    if (validateFields()) {
      onNext();
    }
  };

  const handleCountrySelect = (country: string) => {
    updateData({ 
      country, 
      state: '', 
      city: '' 
    });
  };

  const handleStateSelect = (state: string) => {
    updateData({ 
      state, 
      city: '' 
    });
  };

  const handleCitySelect = (city: string) => {
    updateData({ city });
  };

  const countryNames = countries.map(country => country.name);
  const stateNames = states.map(state => state.name);
  const cityNames = cities.map(city => city.name);

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      <View style={styles.header}>
        <TouchableOpacity onPress={onBack} style={styles.backButton}>
          <MaterialIcons name="arrow-back" size={24} color={COLORS.primary} />
        </TouchableOpacity>
        <Text style={styles.title}>Personal Details</Text>
      </View>

      {error && (
        <View style={styles.errorContainer}>
          <MaterialIcons name="error" size={20} color={COLORS.danger} />
          <Text style={styles.errorText}>
            {error}. Using limited country list.
          </Text>
        </View>
      )}

      {/* Profile Photo Upload */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Profile Photo</Text>
        <TouchableOpacity onPress={pickImage} style={styles.profilePhotoContainer}>
          {data.profilePhoto ? (
            <Image source={{ uri: data.profilePhoto }} style={styles.profileImage} />
          ) : (
            <View style={styles.uploadPlaceholder}>
              <MaterialIcons name="add-a-photo" size={40} color={COLORS.primary} />
              <Text style={styles.uploadText}>Add Profile Photo</Text>
            </View>
          )}
        </TouchableOpacity>
      </View>

      {/* Personal Information Section */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Personal Information</Text>
        
        <View style={styles.inputContainer}>
          <MaterialIcons name="person" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Full Name *"
            value={data.name}
            onChangeText={(text) => updateData({ name: text })}
            autoCapitalize="words"
            editable={!isLoading}
          />
        </View>

        <View style={styles.inputContainer}>
          <MaterialIcons name="alternate-email" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Username *"
            value={data.username}
            onChangeText={(text) => updateData({ username: text })}
            autoCapitalize="none"
            editable={!isLoading}
          />
        </View>

        <View style={styles.inputContainer}>
          <MaterialIcons name="location-on" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <View style={styles.pickerWrapper}>
            <CustomPickerWithSearch
              data={countryNames}
              selectedItem={data.country}
              setSelectedItem={handleCountrySelect}
              backgroundColor="#FFF"
              borderColor={COLORS.lightGray}
              borderWidth={1}
              placeholder="Select Country *"
              disabled={isLoading || loadingCountries}
            />
            {loadingCountries && (
              <ActivityIndicator 
                size="small" 
                color={COLORS.primary} 
                style={styles.loadingIndicator}
              />
            )}
          </View>
        </View>

        <View style={styles.row}>
          <View style={[styles.inputContainer, { flex: 1, marginRight: 10 }]}>
            <MaterialIcons name="map" size={20} color={COLORS.gray} style={styles.inputIcon} />
            <View style={styles.pickerWrapper}>
              <CustomPickerWithSearch
                data={stateNames}
                selectedItem={data.state}
                setSelectedItem={handleStateSelect}
                backgroundColor="#FFF"
                borderColor={COLORS.lightGray}
                borderWidth={1}
                placeholder="State/Province"
                disabled={!data.country || isLoading || loadingStates}
              />
              {loadingStates && (
                <ActivityIndicator 
                  size="small" 
                  color={COLORS.primary} 
                  style={styles.loadingIndicator}
                />
              )}
            </View>
          </View>

          <View style={[styles.inputContainer, { flex: 1 }]}>
            <MaterialIcons name="location-city" size={20} color={COLORS.gray} style={styles.inputIcon} />
            <View style={styles.pickerWrapper}>
              <CustomPickerWithSearch
                data={cityNames}
                selectedItem={data.city}
                setSelectedItem={handleCitySelect}
                backgroundColor="#FFF"
                borderColor={COLORS.lightGray}
                borderWidth={1}
                placeholder="City/LGA"
                disabled={!data.state || isLoading || loadingCities}
              />
              {loadingCities && (
                <ActivityIndicator 
                  size="small" 
                  color={COLORS.primary} 
                  style={styles.loadingIndicator}
                />
              )}
            </View>
          </View>
        </View>

        <View style={styles.inputContainer}>
          <MaterialIcons name="home" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={[styles.input, styles.multilineInput]}
            placeholder="Residential Address *"
            value={data.address}
            onChangeText={(text) => updateData({ address: text })}
            multiline
            numberOfLines={3}
            editable={!isLoading}
          />
        </View>
      </View>

      {/* Contact Information Section */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Contact Information</Text>
        
        <PhoneNumber
          inputValue={data.phoneNumber}
          setInputValue={(value) => updateData({ phoneNumber: value })}
          //@ts-expect-error
          selectedCountry={selectedCountry}
          //@ts-expect-error
          setSelectedCountry={setSelectedCountry}
          placeholder="Phone Number *"
          disabled={isLoading}
        />

        <View style={styles.inputContainer}>
          <MaterialIcons name="email" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Email Address *"
            value={data.email}
            onChangeText={(text) => updateData({ email: text })}
            keyboardType="email-address"
            autoCapitalize="none"
            editable={!isLoading}
          />
        </View>
      </View>

      {/* Login Credentials Section */}
      <View style={styles.section}>
        <Text style={styles.sectionTitle}>Login Credentials</Text>
        
        <View style={styles.inputContainer}>
          <MaterialIcons name="lock" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Password * (min. 8 characters)"
            value={data.password}
            onChangeText={(text) => updateData({ password: text })}
            secureTextEntry={!showPassword}
            editable={!isLoading}
          />
          <TouchableOpacity
            onPress={() => setShowPassword(!showPassword)}
            style={styles.passwordIcon}
            disabled={isLoading}
          >
            <MaterialIcons
              name={showPassword ? 'visibility' : 'visibility-off'}
              size={20}
              color={COLORS.gray}
            />
          </TouchableOpacity>
        </View>

        <View style={styles.inputContainer}>
          <MaterialIcons name="lock-outline" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Confirm Password *"
            value={data.confirmPassword}
            onChangeText={(text) => updateData({ confirmPassword: text })}
            secureTextEntry={!showConfirmPassword}
            editable={!isLoading}
          />
          <TouchableOpacity
            onPress={() => setShowConfirmPassword(!showConfirmPassword)}
            style={styles.passwordIcon}
            disabled={isLoading}
          >
            <MaterialIcons
              name={showConfirmPassword ? 'visibility' : 'visibility-off'}
              size={20}
              color={COLORS.gray}
            />
          </TouchableOpacity>
        </View>

        {data.password.length > 0 && (
          <View style={styles.passwordStrengthContainer}>
            <Text style={styles.passwordStrengthLabel}>Password Strength:</Text>
            <View style={styles.strengthBarContainer}>
              <View style={[
                styles.strengthBar,
                { 
                  flex: data.password.length >= 8 ? 1 : 0.5,
                  backgroundColor: data.password.length >= 8 ? COLORS.success : COLORS.warning 
                }
              ]} />
            </View>
            {data.password.length < 8 && (
              <Text style={styles.passwordStrengthHint}>
                Password should be at least 8 characters long
              </Text>
            )}
          </View>
        )}
      </View>

      {/* Referral Code Section */}
      <View style={styles.section}>
        <View style={styles.inputContainer}>
          <MaterialIcons name="card-giftcard" size={20} color={COLORS.gray} style={styles.inputIcon} />
          <TextInput
            style={styles.input}
            placeholder="Referral Code (Optional)"
            value={data.referralCode}
            onChangeText={(text) => updateData({ referralCode: text })}
            editable={!isLoading}
          />
        </View>
      </View>

      {(loadingStates || loadingCities) && (
        <View style={styles.infoNote}>
          <MaterialIcons name="info" size={16} color={COLORS.primary} />
          <Text style={styles.infoText}>
            {loadingStates ? 'Loading states...' : 'Loading cities...'} 
            This may take a moment.
          </Text>
        </View>
      )}

      {/* Next Button */}
      <TouchableOpacity 
        style={[styles.nextButton, (isLoading || loadingCountries || loadingStates || loadingCities) && styles.nextButtonDisabled]} 
        onPress={handleNext}
        disabled={isLoading || loadingCountries || loadingStates || loadingCities}
      >
        {isLoading ? (
          <ActivityIndicator size="small" color="#fff" />
        ) : (
          <>
            <Text style={styles.nextButtonText}>Next</Text>
            <MaterialIcons name="arrow-forward" size={20} color="#fff" />
          </>
        )}
      </TouchableOpacity>

      <View style={styles.footer}>
        <Text style={styles.footerText}>
          Already have an account?{' '}
          <TouchableOpacity onPress={() => router.push('/login')} disabled={isLoading}>
            <Text style={styles.loginLink}>Login</Text>
          </TouchableOpacity>
        </Text>
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
  section: {
    marginBottom: 30,
  },
  sectionTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 15,
    paddingBottom: 8,
    borderBottomWidth: 1,
    borderBottomColor: COLORS.lightGray,
  },
  profilePhotoContainer: {
    alignItems: 'center',
    marginBottom: 20,
  },
  profileImage: {
    width: 120,
    height: 120,
    borderRadius: 60,
    borderWidth: 3,
    borderColor: COLORS.primary,
  },
  uploadPlaceholder: {
    width: 120,
    height: 120,
    borderRadius: 60,
    backgroundColor: COLORS.lightPrimary,
    justifyContent: 'center',
    alignItems: 'center',
    borderWidth: 2,
    borderColor: COLORS.primary,
    borderStyle: 'dashed',
  },
  uploadText: {
    marginTop: 8,
    color: COLORS.primary,
    fontSize: 12,
  },
  inputContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: '#fff',
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.lightGray,
    marginBottom: 15,
    paddingHorizontal: 15,
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
  multilineInput: {
    height: 80,
    textAlignVertical: 'top',
    paddingTop: 15,
  },
  passwordIcon: {
    padding: 10,
  },
  row: {
    flexDirection: 'row',
  },
  pickerWrapper: {
    flex: 1,
    position: 'relative',
  },
  loadingIndicator: {
    position: 'absolute',
    right: 10,
    top: '50%',
    transform: [{ translateY: -10 }],
  },
  errorContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(239, 68, 68, 0.1)',
    borderRadius: 8,
    padding: 12,
    marginBottom: 15,
  },
  errorText: {
    marginLeft: 8,
    color: COLORS.danger,
    fontSize: 14,
    flex: 1,
  },
  nextButton: {
    flexDirection: 'row',
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
  nextButtonDisabled: {
    backgroundColor: COLORS.lightGray,
    opacity: 0.7,
  },
  nextButtonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
    marginRight: 10,
  },
  footer: {
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
    marginLeft: 5,
  },
  infoNote: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(99, 102, 241, 0.1)',
    borderRadius: 8,
    padding: 12,
    marginBottom: 15,
  },
  infoText: {
    marginLeft: 8,
    color: COLORS.primary,
    fontSize: 14,
    flex: 1,
  },
  passwordStrengthContainer: {
    marginTop: 10,
    padding: 12,
    backgroundColor: 'rgba(16, 185, 129, 0.1)',
    borderRadius: 8,
  },
  passwordStrengthLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 6,
  },
  strengthBarContainer: {
    height: 4,
    backgroundColor: COLORS.lightGray,
    borderRadius: 2,
    marginBottom: 6,
    overflow: 'hidden',
  },
  strengthBar: {
    height: '100%',
    borderRadius: 2,
  },
  passwordStrengthHint: {
    fontSize: 12,
    color: COLORS.gray,
    fontStyle: 'italic',
  },
});

export default SignupScreen1;