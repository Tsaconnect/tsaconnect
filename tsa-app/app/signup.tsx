import React, { useEffect } from 'react'
import { useAuth } from '../AuthContext/AuthContext';
import { router } from 'expo-router';
import SignupFlow from '@/components/signup/signupflow';

const signup = () => {
  const { isAuthenticated } = useAuth();
  useEffect(() => {
    if (isAuthenticated) {
      //@ts-ignore
      router.push('/home')
    }
  }, [])
  return <SignupFlow />
}

export default signup
