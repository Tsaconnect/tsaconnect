import React, { useState } from 'react';
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  TextInput,
  Alert,
} from 'react-native';
import { Avatar } from 'react-native-elements';
import { useRouter } from 'expo-router';
import { COLORS, SIZES } from '../../constants';
import LocationPicker from '../common/LocationPicker';

import PhoneNumber from '../country/phoneNumber';
import axios from 'axios';
import { baseUrl } from '../../constants/api/apiClient';
import { useAuth } from '../../AuthContext/AuthContext';
import * as ImagePicker from 'expo-image-picker';
import { generateFileName } from '../../constants/api/filename';
import Icon from '@expo/vector-icons/FontAwesome';
//@ts-ignore
const EditProfileScreen = ({ user }) => {
  const { token } = useAuth();
  const [name, setName] = useState(user.name);
  const [phoneNumber, setPhoneNumber] = useState(user.phoneNumber);
  const [location, setLocation] = useState({
    country: user.country || '',
    state: user.state || '',
    city: user.city || '',
  });
  const [selectedCountry, setSelectedCountry] = useState();
  const [profileImage, setProfileImage] = useState(user.profilePicture);
  const [imageUri, setImageUri] = useState(null);

  const router = useRouter();

  const removeAllSpaces = (str: string) => str.replace(/\s+/g, '');

  const pickImageAsync = async () => {
    try {
      const { status } =
        await ImagePicker.requestMediaLibraryPermissionsAsync();
      if (status !== 'granted') {
        alert('Permission to access media library denied');
        return;
      }

      let result = await ImagePicker.launchImageLibraryAsync({
        allowsEditing: false,
        aspect: [4, 3],
        quality: 1,
      });

      if (!result.canceled) {
        setProfileImage(result.assets[0].uri);
        //@ts-ignore
        setImageUri(result.assets[0].uri);
      } else {
        alert('You did not select any image.');
      }
    } catch (error) {
      console.error('Error picking image:', error);
    }
  };

  const handleUpdate = async () => {
    const formData = new FormData();
    if (imageUri) {
      //@ts-ignore
      formData.append('profilePicture', {
        uri: imageUri,
        type: 'image/jpeg',
        name: `image_${generateFileName()}.jpg`,
      });
    }

    formData.append('name', name);
    formData.append(
      'phoneNumber',
      //@ts-ignore
      removeAllSpaces(selectedCountry?.callingCode + phoneNumber) ||
        phoneNumber,
    );
    formData.append('country', location.country);
    formData.append('state', location.state);
    formData.append('city', location.city);

    try {
      const { data } = await axios.patch(
        `${baseUrl}/users/${user.id}`,
        formData,
        {
          headers: {
            Authorization: `${token}`,
            'Content-Type': 'multipart/form-data',
          },
        },
      );
      alert('Information Successfully changed');
      router.push('/profile');
    } catch (error: any) {
      Alert.alert(
        'Error',
        'Failed to update. ' + error?.response?.data?.message || error.message,
      );
    }
  };

  return (
    <View style={styles.container}>
      <View style={styles.cover}>
        <View style={styles.profileInfo}>
          {profileImage ? (
            <Avatar rounded source={{ uri: profileImage }} size="large" />
          ) : (
            <Avatar
              rounded
              title={user.name[0]}
              size="large"
              overlayContainerStyle={{ backgroundColor: '#9D6B38' }}
              titleStyle={{ color: '#fff' }}
            />
          )}
          <TouchableOpacity
            style={styles.uploadContainer}
            onPress={pickImageAsync}
          >
            <Icon name="upload" size={25} color="#aaa" />
            <Text style={styles.uploadText}>Change Profile Image</Text>
          </TouchableOpacity>
        </View>

        <TextInput
          placeholder="Your Name"
          style={styles.input}
          value={name}
          onChangeText={setName}
        />

        <LocationPicker
          value={location}
          onChange={setLocation}
          required={['country']}
        />

        <PhoneNumber
          inputValue={phoneNumber}
          setInputValue={setPhoneNumber}
          //@ts-ignore
          selectedCountry={selectedCountry}
          //@ts-ignore
          setSelectedCountry={setSelectedCountry}
        />

        <View style={styles.buttonContainer}>
          <TouchableOpacity style={styles.button} onPress={handleUpdate}>
            <Text style={styles.buttonText}>Update</Text>
          </TouchableOpacity>
        </View>
      </View>
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: 'center',
    padding: 20,
    backgroundColor: '#fff',
  },
  input: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: 'gray',
    borderWidth: 1,
    marginTop: 10,
    padding: 10,
    borderRadius: 10,
    marginHorizontal: SIZES.width * 0.05,
  },
  buttonContainer: {
    alignItems: 'center',
    justifyContent: 'center',
  },
  button: {
    backgroundColor: COLORS.primary,
    alignItems: 'center',
    justifyContent: 'center',
    height: 45,
    borderRadius: 5,
    width: SIZES.width * 0.9,
  },
  buttonText: {
    color: '#fff',
    fontSize: 16,
  },
  profileInfo: {
    alignItems: 'center',
    justifyContent: 'center',
    marginTop: 15,
  },
  uploadContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginTop: 10,
  },
  uploadText: {
    marginLeft: 10,
    color: '#aaa',
  },
  cover: {
    position: 'absolute',
    top: SIZES.height * 0.1212,
    marginBottom: 15,
    backgroundColor: '#FFF',
  },
});

export default EditProfileScreen;
