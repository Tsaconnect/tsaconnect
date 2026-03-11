import { StyleSheet, Text, View } from "react-native";
import React from "react";
import EditProfileScreen from "../../components/profile/EditProfile";
import { useLocalSearchParams } from "expo-router";

const EditProfile = () => {
  const user = useLocalSearchParams();
  return (
    <View>
      <EditProfileScreen user={user} />
    </View>
  );
};

export default EditProfile;

const styles = StyleSheet.create({});
