import {
  ScrollView,
  StyleSheet,
  Text,
  View,
  ActivityIndicator,
} from "react-native";
import React, { useState, useEffect } from "react";
import ServiceCard from "../../components/services/ServiceCard";
import { router, useLocalSearchParams } from "expo-router";
import { baseUrl } from "../../constants/api/apiClient";
import axios from "axios";
import { useAuth } from "../../AuthContext/AuthContext";
import { COLORS } from "../../constants";

const Services = () => {
  const { value } = useLocalSearchParams();
  const [services, setServices] = useState([]);
  const [loading, setLoading] = useState(true);
  const { token, setAppService } = useAuth();

  useEffect(() => {
    const fetchedServices = async () => {
      try {
        const response = await axios.get(`${baseUrl}/adverts?type=Service`, {
          headers: {
            Authorization: `${token}`,
          },
        });
        const fetchServices = response.data.results;
        setServices(fetchServices);
      } catch (error) {
        //@ts-ignore
        alert(error?.response.data.message);
      } finally {
        setLoading(false); // Set loading to false once data is fetched
      }
    };
    if (token) {
      fetchedServices();
    }
  }, []);
  //@ts-ignore
  const filteredData = services.filter((item) => item.category === value);

  return (
    <View style={{ backgroundColor: "#fff", flex: 1, alignItems: "center" }}>
      <ScrollView>
        {loading ? ( // Show loading spinner when loading
          <View style={styles.loadingContainer}>
            <ActivityIndicator size="large" color="#E8A14A" />
          </View>
        ) : filteredData.length > 0 ? (
          filteredData.map((item, index) => (
            <ServiceCard
              key={index}
              //@ts-ignore
              id={item._id || item.id}
              //@ts-ignore
              title={item.name}
              //@ts-ignore
              description={item.description}
              //@ts-ignore
              image={item.images[0]}
            />
          ))
        ) : (
          <View style={styles.noResultsContainer}>
            <View style={styles.notFoundContainer}>
              <Text style={styles.notFoundText}>
                No registered available vendor merchant at the moment.
              </Text>
            </View>
          </View>
        )}
      </ScrollView>
      <View
        style={{
          position: "absolute",
          bottom: 5,
          justifyContent: "center",
          alignContent: "center",
        }}
      >
        <Text
          onPress={() => {
            setAppService("Register your service");
            router.push({ pathname: "/serviceaction", params: { index: 1 } });
          }}
          style={{ color: COLORS.primary, fontWeight: "500" }}
        >
          Click here to register as a vendor
        </Text>
      </View>
    </View>
  );
};

export default Services;

const styles = StyleSheet.create({
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
    height: "100%",
  },
  noResultsContainer: {
    display: "flex",
    justifyContent: "center",
    alignItems: "center",
    marginTop: 20,
  },
  noResultsText: {
    fontSize: 16,
    color: "#555",
  },
  container: {
    flex: 1,
    alignItems: "center",
  },
  notFoundContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  notFoundText: {
    fontSize: 14,
  },
  subText: {
    fontSize: 14,
  },
  registerLink: {
    fontSize: 16,
    color: "#E8A14A",
    marginTop: 15,
  },
});
