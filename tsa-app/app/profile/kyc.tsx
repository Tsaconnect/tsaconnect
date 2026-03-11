import React, { useState } from "react";
import {
  StyleSheet,
  View,
  Text,
  TextInput,
  TouchableOpacity,
  Alert,
  ScrollView,
  Image,
} from "react-native";
import axios from "axios";
import { useAuth } from "../../AuthContext/AuthContext";
import { baseUrl } from "../../constants/api/apiClient";
import { Picker } from "@react-native-picker/picker";
import * as ImagePicker from "expo-image-picker";
import CustomDatePicker from "../../components/others/CustomDatePicker";
import { COLORS } from "../../constants";

const KycVerificationScreen = () => {
  const { token } = useAuth();
  const [form, setForm] = useState({
    firstName: "",
    lastName: "",
    middleName: "",
    dob: "",
    resCountry: "Nigeria",
    resState: "",
    resCity: "",
    resAddress: "",
    resPostalCode: "",
    selfiePhotoFile: null,
    idPhotoFile: null,
    idType: "",
    idNumber: "",
    issueDate: "",
    expiryDate: "",
  });

  const [firstName, setFirstName] = useState();

  const [middleName, setMiddleName] = useState();
  const [lastName, setLastName] = useState();
  const [dob, setDob] = useState();
  const [resCountry, setResCountry] = useState();
  const [resState, setResState] = useState();
  const [resCity, setResCity] = useState();
  const [resAddress, setResAddress] = useState();
  const [idPhoto, setIdPhoto] = useState();
  const [posterCode, setPosterCode] = useState();
  const [selfiePhoto, setSelfiePhote] = useState();
  const [idtType, setIdType] = useState();

  const [loading, setLoading] = useState(false);
  const [stepOne, setStepOne] = useState(true);
  const [stepTwo, setStepTwo] = useState(false);
  const [stepThree, setStepThree] = useState(false);
  const [stepFour, setStepFour] = useState(false);
  const handleImagePicker = async (field) => {
    let result = await ImagePicker.launchImageLibraryAsync({
      mediaTypes: ImagePicker.MediaTypeOptions.Images,
      allowsEditing: true,
      aspect: [4, 3],
      quality: 1,
    });

    if (!result.canceled) {
      setForm({ ...form, [field]: result.uri });
    }
  };

  const handleCamera = async (field) => {
    let result = await ImagePicker.launchCameraAsync({
      allowsEditing: true,
      aspect: [4, 3],
      quality: 1,
    });

    if (!result.canceled) {
      setForm({ ...form, [field]: result.uri });
    }
  };

  const handleSubmit = async () => {
    try {
      setLoading(true);
      const formData = new FormData();
      for (const key in form) {
        if (key === "selfiePhotoFile" || key === "idPhotoFile") {
          if (form[key]) {
            formData.append(key, {
              uri: form[key],
              type: "image/jpeg",
              name: `${key}.jpg`,
            });
          }
        } else {
          formData.append(key, form[key]);
        }
      }
      await axios.post(`${baseUrl}/kyc`, formData, {
        headers: {
          Authorization: `${token}`,
          "Content-Type": "multipart/form-data",
        },
      });
      Alert.alert("Success", "KYC verification submitted successfully!");
    } catch (error) {
      Alert.alert(
        "Error",
        error?.response?.data?.message || "Failed to submit KYC verification."
      );
    } finally {
      setLoading(false);
    }
  };

  const idTypesNigeria = [
    { label: "National ID", value: "National ID" },
    { label: "Driver's License", value: "Driver's License" },
    { label: "Voter's Card", value: "Voter's Card" },
    { label: "Passport", value: "Passport" },
  ];

  const idTypesOther = [
    { label: "Passport", value: "Passport" },
    { label: "ID Card", value: "ID Card" },
  ];

  const idTypes = form.resCountry === "Nigeria" ? idTypesNigeria : idTypesOther;

  return (
    <ScrollView contentContainerStyle={styles.container}>
      <Text style={styles.title}>KYC Verification</Text>
      {stepOne && (
        <View>
          <TextInput
            style={styles.input}
            placeholder="First Name"
            value={form.firstName}
            onChangeText={(value) => setForm({ ...form, firstName: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="Middle Name"
            value={form.middleName}
            onChangeText={(value) => setForm({ ...form, middleName: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="Last Name"
            value={form.lastName}
            onChangeText={(value) => setForm({ ...form, lastName: value })}
          />

          <CustomDatePicker
            date={form.dob}
            placeholder="Date of Birth"
            onDateChange={(date) => setForm({ ...form, dob: date })}
          />
          <TouchableOpacity
            onPress={() => {
              setStepOne(false);
              setStepTwo(true);
            }}
            style={styles.nextButton}
          >
            <Text style={{ padding: 10, fontWeight: "600" }}>Next</Text>
          </TouchableOpacity>
        </View>
      )}
      {stepTwo && (
        <View>
          <TextInput
            style={styles.input}
            placeholder="Residential Country"
            value={form.resCountry}
            onChangeText={(value) => setForm({ ...form, resCountry: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="State"
            value={form.resState}
            onChangeText={(value) => setForm({ ...form, resState: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="City"
            value={form.resCity}
            onChangeText={(value) => setForm({ ...form, resCity: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="Address"
            value={form.resAddress}
            onChangeText={(value) => setForm({ ...form, resAddress: value })}
          />

          <TextInput
            style={styles.input}
            placeholder="Postal Code"
            value={form.resPostalCode}
            onChangeText={(value) => setForm({ ...form, resPostalCode: value })}
          />
          <TouchableOpacity
            onPress={() => {
              setStepOne(false);
              setStepTwo(false);
              setStepThree(true);
            }}
            style={styles.nextButton}
          >
            <Text style={{ padding: 10, fontWeight: "600" }}>Next</Text>
          </TouchableOpacity>
        </View>
      )}
      {stepThree && (
        <View>
          <Picker
            selectedValue={form.idType}
            style={styles.input}
            onValueChange={(itemValue) =>
              setForm({ ...form, idType: itemValue })
            }
          >
            <Picker.Item label="Select ID Type" value="" />
            {idTypes.map((type) => (
              <Picker.Item
                key={type.value}
                label={type.label}
                value={type.value}
              />
            ))}
          </Picker>

          <TextInput
            style={styles.input}
            placeholder="ID Number"
            value={form.idNumber}
            onChangeText={(value) => setForm({ ...form, idNumber: value })}
          />

          <CustomDatePicker
            date={form.issueDate}
            placeholder="Issue Date"
            onDateChange={(date) => setForm({ ...form, issueDate: date })}
          />

          <CustomDatePicker
            date={form.expiryDate}
            placeholder="Expiry Date"
            onDateChange={(date) => setForm({ ...form, expiryDate: date })}
          />
          <TouchableOpacity
            onPress={() => {
              setStepOne(false);
              setStepTwo(false);
              setStepThree(false);
              setStepFour(true);
            }}
            style={styles.nextButton}
          >
            <Text style={{ padding: 10, fontWeight: "600" }}>Next</Text>
          </TouchableOpacity>
        </View>
      )}
      {stepFour && (
        <>
          <View style={styles.imagePickerContainer}>
            <Text style={styles.label}>Selfie Photo</Text>
            <View style={styles.imagePickerButtons}>
              <TouchableOpacity
                style={styles.button}
                onPress={() => handleImagePicker("selfiePhotoFile")}
              >
                <Text style={styles.buttonText}>Upload</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={styles.button}
                onPress={() => handleCamera("selfiePhotoFile")}
              >
                <Text style={styles.buttonText}>Snap</Text>
              </TouchableOpacity>
            </View>
            {form.selfiePhotoFile && (
              <Image
                source={{ uri: form.selfiePhotoFile }}
                style={styles.imagePreview}
              />
            )}
          </View>

          <View style={styles.imagePickerContainer}>
            <Text style={styles.label}>ID Photo</Text>
            <View style={styles.imagePickerButtons}>
              <TouchableOpacity
                style={styles.button}
                onPress={() => handleImagePicker("idPhotoFile")}
              >
                <Text style={styles.buttonText}>Upload</Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={styles.button}
                onPress={() => handleCamera("idPhotoFile")}
              >
                <Text style={styles.buttonText}>Snap</Text>
              </TouchableOpacity>
            </View>
            {form.idPhotoFile && (
              <Image
                source={{ uri: form.idPhotoFile }}
                style={styles.imagePreview}
              />
            )}
          </View>

          <TouchableOpacity
            style={styles.submitButton}
            onPress={handleSubmit}
            disabled={loading}
          >
            <Text style={styles.submitButtonText}>
              {loading ? "Submitting..." : "Submit"}
            </Text>
          </TouchableOpacity>
        </>
      )}
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flexGrow: 1,
    padding: 20,
    backgroundColor: "#fff",
  },
  title: {
    fontSize: 24,
    fontWeight: "bold",
    marginBottom: 20,
    textAlign: "center",
  },
  input: {
    width: "100%",
    padding: 10,
    borderWidth: 1,
    borderColor: "#ccc",
    borderRadius: 5,
    marginBottom: 15,
  },
  datePicker: {
    width: "100%",
    marginBottom: 15,
  },
  imagePickerContainer: {
    marginBottom: 15,
  },
  label: {
    fontSize: 16,
    fontWeight: "bold",
    marginBottom: 5,
  },
  imagePickerButtons: {
    flexDirection: "row",
    justifyContent: "space-between",
  },
  button: {
    backgroundColor: "#9D6B38",
    padding: 10,
    borderRadius: 5,
    width: "48%",
    alignItems: "center",
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  imagePreview: {
    width: "100%",
    height: 200,
    marginTop: 10,
  },
  submitButton: {
    backgroundColor: "#9D6B38",
    padding: 15,
    borderRadius: 5,
    alignItems: "center",
  },
  submitButtonText: {
    color: "#fff",
    fontSize: 16,
  },
  nextButton: {
    backgroundColor: COLORS.lightButton,
    borderRadius: 10,
    alignItems: "center",
  },
});

export default KycVerificationScreen;
