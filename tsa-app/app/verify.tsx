import React, { useEffect } from "react";
import Verify from "../components/onboarding/Verify";
import { router, useLocalSearchParams } from "expo-router";
import { useAuth } from "../AuthContext/AuthContext";

const VerifySignup = () => {
  const payLoad = useLocalSearchParams();
  const { isAuthenticated } = useAuth();

  useEffect(() => {
    if (isAuthenticated) {
      //@ts-ignore
      router.push("/home");
    }
  }, []);
  return <Verify payLoad={payLoad} />;
};

export default VerifySignup;
