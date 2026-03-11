import { StyleSheet, Text, View } from 'react-native'
import React, { useContext } from 'react'
import { Stack } from "expo-router";
import { AppContext } from '../../AuthContext/AuthContext';
const Layout = () => {
    const {category}=useContext(AppContext)
  return (
    <Stack>
      <Stack.Screen
        name="categoryservice"
        options={{
          headerTitle: `${category} Services`,
        }}
      />
      <Stack.Screen
        name="servicedetail"
        options={{
          headerTitle: `${category} Service Details`,
        }}
      />
      </Stack>
  )
}

export default Layout

const styles = StyleSheet.create({})