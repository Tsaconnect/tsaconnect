// components/signup/SignupScreen3.tsx
import React, { useState, useRef, useEffect } from 'react';
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  Image,
  ActivityIndicator,
  Alert,
} from 'react-native';
import { CameraView, useCameraPermissions } from 'expo-camera';
import MaterialIcons from '@expo/vector-icons/MaterialIcons';
import { COLORS, SIZES } from '../../constants/theme';
import { SignupData } from './signup';

interface SignupScreen3Props {
  data: SignupData;
  updateData: (data: Partial<SignupData>) => void;
  onNext: () => void;
  onBack: () => void;
  isLoading?: boolean;
  authToken?: string | null;
  userId?: string | null;
}

const FACIAL_ANGLES = [
  { key: 'faceFront', label: 'Front Face', icon: 'face', instruction: 'Look straight at the camera' },
  { key: 'faceLeft', label: 'Left Angle', icon: 'rotate-left', instruction: 'Turn your head slightly left' },
  { key: 'faceRight', label: 'Right Angle', icon: 'rotate-right', instruction: 'Turn your head slightly right' },
  { key: 'faceUp', label: 'Upward Angle', icon: 'arrow-upward', instruction: 'Look slightly upward' },
  { key: 'faceDown', label: 'Downward Angle', icon: 'arrow-downward', instruction: 'Look slightly downward' },
];

const SignupScreen3: React.FC<SignupScreen3Props> = ({
  data,
  updateData,
  onNext,
  onBack,
  isLoading = false,
  authToken,
  userId,
}) => {
  const [permission, requestPermission] = useCameraPermissions();
  const [currentAngleIndex, setCurrentAngleIndex] = useState(0);
  const [capturing, setCapturing] = useState(false);
  const [cameraReady, setCameraReady] = useState(false);
  const cameraRef = useRef<CameraView>(null);

  // Reset to first uncaptured angle when data changes
  useEffect(() => {
    const uncapturedIndex = FACIAL_ANGLES.findIndex(angle => !data[angle.key as keyof SignupData]);
    if (uncapturedIndex !== -1) {
      setCurrentAngleIndex(uncapturedIndex);
    } else {
      setCurrentAngleIndex(FACIAL_ANGLES.length - 1);
    }
  }, [data]);

  if (!permission) {
    return (
      <View style={styles.permissionContainer}>
        <ActivityIndicator size="large" color={COLORS.primary} />
      </View>
    );
  }

  if (!permission.granted) {
    return (
      <View style={styles.permissionContainer}>
        <MaterialIcons name="camera" size={80} color={COLORS.primary} />
        <Text style={styles.permissionTitle}>Camera Permission Required</Text>
        <Text style={styles.permissionText}>
          We need camera access to capture facial verification images
        </Text>
        <TouchableOpacity 
          style={styles.permissionButton} 
          onPress={requestPermission}
          disabled={isLoading}
        >
          <Text style={styles.permissionButtonText}>Grant Permission</Text>
        </TouchableOpacity>
      </View>
    );
  }

  const takePicture = async () => {
    if (!cameraRef.current || capturing || !cameraReady) return;

    setCapturing(true);
    try {
      const photo = await cameraRef.current.takePictureAsync({
        quality: 0.8,
        base64: false,
      });

      const currentAngle = FACIAL_ANGLES[currentAngleIndex];
      updateData({ [currentAngle.key]: photo.uri });

      // Move to next uncaptured angle
      const nextUncapturedIndex = FACIAL_ANGLES.findIndex(
        (angle, index) => index > currentAngleIndex && !data[angle.key as keyof SignupData]
      );
      
      if (nextUncapturedIndex !== -1) {
        setCurrentAngleIndex(nextUncapturedIndex);
      } else {
        // All angles captured
        Alert.alert(
          'Success',
          'All facial angles captured! You can review them below.',
          [{ text: 'OK' }]
        );
      }
    } catch (error) {
      console.error('Error taking picture:', error);
      Alert.alert('Error', 'Failed to capture image. Please try again.');
    } finally {
      setCapturing(false);
    }
  };

  const retakeAngle = (angleKey: string) => {
    const angleIndex = FACIAL_ANGLES.findIndex(angle => angle.key === angleKey);
    if (angleIndex !== -1) {
      setCurrentAngleIndex(angleIndex);
      updateData({ [angleKey]: undefined });
    }
  };

  const currentAngle = FACIAL_ANGLES[currentAngleIndex];
  const allAnglesCaptured = FACIAL_ANGLES.every(angle => data[angle.key as keyof SignupData]);

  const handleComplete = () => {
    if (!allAnglesCaptured) {
      Alert.alert(
        'Incomplete',
        'Please capture all 5 facial angles before proceeding.',
        [{ text: 'OK' }]
      );
      return;
    }

    if (authToken && userId) {
      Alert.alert(
        'Ready to Submit',
        'All facial verification images are captured. Ready to complete your signup?',
        [
          { text: 'Review', style: 'cancel' },
          { 
            text: 'Complete Signup', 
            onPress: onNext,
            style: 'default'
          }
        ]
      );
    } else {
      Alert.alert(
        'Session Expired',
        'Your session has expired. Please go back and start again.',
        [
          { 
            text: 'Go Back', 
            onPress: onBack 
          }
        ]
      );
    }
  };

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      <View style={styles.header}>
        <TouchableOpacity onPress={onBack} style={styles.backButton} disabled={isLoading}>
          <MaterialIcons name="arrow-back" size={24} color={isLoading ? COLORS.lightGray : COLORS.primary} />
        </TouchableOpacity>
        <Text style={styles.title}>Facial Verification</Text>
        <Text style={styles.subtitle}>
          Capture {FACIAL_ANGLES.length} different angles for verification
        </Text>
        
        {authToken && userId && (
          <View style={styles.authStatus}>
            <MaterialIcons name="verified" size={16} color={COLORS.success} />
            <Text style={styles.authStatusText}>Step 2 completed ✓</Text>
          </View>
        )}
      </View>

      {/* Progress Indicator */}
      <View style={styles.progressContainer}>
        <View style={styles.progressBar}>
          {FACIAL_ANGLES.map((angle, index) => (
            <View
              key={index}
              style={[
                styles.progressDot,
                index <= currentAngleIndex && styles.progressDotActive,
                data[angle.key as keyof SignupData] && styles.progressDotCaptured,
              ]}
            />
          ))}
        </View>
        <Text style={styles.progressText}>
          {FACIAL_ANGLES.filter(angle => data[angle.key as keyof SignupData]).length} of {FACIAL_ANGLES.length} captured
        </Text>
      </View>

      {/* Camera View */}
      <View style={styles.cameraContainer}>
        <CameraView
          ref={cameraRef}
          style={styles.camera}
          facing="front"
          mode="picture"
          onCameraReady={() => setCameraReady(true)}
        >
          <View style={styles.cameraOverlay}>
            <View style={styles.faceGuideline} />
            <View style={styles.guidelineText}>
              <Text style={styles.guidelineTextContent}>Align face within circle</Text>
            </View>
          </View>
        </CameraView>
        
        {/* Current Angle Instruction */}
        <View style={styles.instructionContainer}>
          <MaterialIcons name={currentAngle.icon as any} size={24} color="#fff" />
          <Text style={styles.instructionText}>{currentAngle.instruction}</Text>
        </View>

        {/* Capture Button */}
        <TouchableOpacity 
          style={[styles.captureButton, (capturing || !cameraReady) && styles.captureButtonDisabled]} 
          onPress={takePicture}
          disabled={capturing || !cameraReady || isLoading}
        >
          {capturing ? (
            <ActivityIndicator size="small" color="#fff" />
          ) : (
            <View style={styles.captureButtonInner} />
          )}
        </TouchableOpacity>
      </View>

      {/* Captured Angles Preview */}
      <View style={styles.previewSection}>
        <View style={styles.previewHeader}>
          <Text style={styles.previewTitle}>Captured Angles</Text>
          <Text style={styles.previewSubtitle}>
            {allAnglesCaptured ? 'All angles captured ✓' : 'Tap any angle to retake'}
          </Text>
        </View>
        <View style={styles.previewGrid}>
          {FACIAL_ANGLES.map((angle, index) => (
            <View key={angle.key} style={styles.previewItem}>
              <View style={styles.previewLabelContainer}>
                <MaterialIcons
                  name={angle.icon as any}
                  size={16}
                  color={data[angle.key as keyof SignupData] ? COLORS.success : COLORS.gray}
                />
                <Text style={[
                  styles.previewLabel,
                  data[angle.key as keyof SignupData] ? styles.previewLabelCaptured : {}
                ]}>
                  {angle.label}
                </Text>
                {currentAngleIndex === index && !data[angle.key as keyof SignupData] && (
                  <View style={styles.currentIndicator}>
                    <MaterialIcons name="lens" size={8} color={COLORS.primary} />
                  </View>
                )}
              </View>
              <TouchableOpacity
                onPress={() => retakeAngle(angle.key)}
                disabled={isLoading || capturing}
                style={styles.previewImageContainer}
              >
                {data[angle.key as keyof SignupData] ? (
                  <Image
                    source={{ uri: data[angle.key as keyof SignupData] as string }}
                    style={styles.previewImage}
                  />
                ) : (
                  <View style={styles.previewPlaceholder}>
                    <MaterialIcons 
                      name="add-a-photo" 
                      size={24} 
                      color={COLORS.lightGray} 
                    />
                    <Text style={styles.previewPlaceholderText}>Pending</Text>
                  </View>
                )}
                {data[angle.key as keyof SignupData] && (
                  <View style={styles.previewOverlay}>
                    <MaterialIcons name="autorenew" size={20} color="#fff" />
                  </View>
                )}
              </TouchableOpacity>
            </View>
          ))}
        </View>
      </View>

      {/* Verification Tips */}
      <View style={styles.tipsContainer}>
        <MaterialIcons name="lightbulb" size={24} color={COLORS.warning} />
        <View style={styles.tipsContent}>
          <Text style={styles.tipsTitle}>Tips for Best Results:</Text>
          <Text style={styles.tipsText}>
            • Ensure good lighting{'\n'}
            • Remove glasses and hats{'\n'}
            • Keep a neutral expression{'\n'}
            • Position face within the guideline
          </Text>
        </View>
      </View>

      {/* Submit Button */}
      <TouchableOpacity
        style={[styles.submitButton, (!allAnglesCaptured || isLoading) && styles.submitButtonDisabled]}
        onPress={handleComplete}
        disabled={!allAnglesCaptured || isLoading}
      >
        {isLoading ? (
          <ActivityIndicator size="small" color="#fff" />
        ) : (
          <>
            <Text style={styles.submitButtonText}>Complete Signup</Text>
            <MaterialIcons name="check-circle" size={20} color="#fff" />
          </>
        )}
      </TouchableOpacity>

      <Text style={styles.securityText}>
        Your facial data is encrypted and used only for verification purposes
      </Text>

      {/* Progress Note */}
      <View style={styles.progressNote}>
        <MaterialIcons name="info" size={16} color={COLORS.primary} />
        <Text style={styles.progressNoteText}>
          Step 3 of 3 - Final Step
        </Text>
      </View>
    </ScrollView>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#fff',
  },
  permissionContainer: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 30,
    backgroundColor: '#fff',
  },
  permissionTitle: {
    fontSize: 24,
    fontWeight: '700',
    color: COLORS.primary,
    marginTop: 20,
    marginBottom: 10,
  },
  permissionText: {
    fontSize: 16,
    color: COLORS.gray,
    textAlign: 'center',
    marginBottom: 30,
    lineHeight: 24,
  },
  permissionButton: {
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    paddingVertical: 16,
    paddingHorizontal: 32,
  },
  permissionButtonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
  },
  header: {
    padding: 20,
  },
  backButton: {
    marginBottom: 20,
  },
  title: {
    fontSize: 28,
    fontWeight: '700',
    color: COLORS.primary,
    marginBottom: 8,
  },
  subtitle: {
    fontSize: 16,
    color: COLORS.gray,
    marginBottom: 8,
  },
  authStatus: {
    flexDirection: 'row',
    alignItems: 'center',
    backgroundColor: 'rgba(16, 185, 129, 0.1)',
    borderRadius: 8,
    padding: 8,
    alignSelf: 'flex-start',
  },
  authStatusText: {
    marginLeft: 6,
    fontSize: 14,
    color: COLORS.success,
    fontWeight: '500',
  },
  progressContainer: {
    alignItems: 'center',
    paddingHorizontal: 20,
    marginBottom: 20,
  },
  progressBar: {
    flexDirection: 'row',
    justifyContent: 'space-between',
    width: '100%',
    marginBottom: 10,
  },
  progressDot: {
    width: 12,
    height: 12,
    borderRadius: 6,
    backgroundColor: COLORS.lightGray,
  },
  progressDotActive: {
    backgroundColor: COLORS.primary,
  },
  progressDotCaptured: {
    backgroundColor: COLORS.success,
  },
  progressText: {
    fontSize: 14,
    color: COLORS.gray,
    fontWeight: '600',
  },
  cameraContainer: {
    height: SIZES.height * 0.4,
    marginHorizontal: 20,
    marginBottom: 30,
    borderRadius: 20,
    overflow: 'hidden',
    backgroundColor: '#000',
  },
  camera: {
    flex: 1,
  },
  cameraOverlay: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
  },
  faceGuideline: {
    width: 200,
    height: 250,
    borderWidth: 2,
    borderColor: 'rgba(255,255,255,0.5)',
    borderRadius: 20,
  },
  guidelineText: {
    position: 'absolute',
    bottom: 20,
  },
  guidelineTextContent: {
    color: 'rgba(255,255,255,0.8)',
    fontSize: 14,
  },
  instructionContainer: {
    position: 'absolute',
    bottom: 100,
    left: 0,
    right: 0,
    flexDirection: 'row',
    justifyContent: 'center',
    alignItems: 'center',
    backgroundColor: 'rgba(0,0,0,0.7)',
    paddingVertical: 15,
  },
  instructionText: {
    color: '#fff',
    fontSize: 16,
    fontWeight: '600',
    marginLeft: 10,
  },
  captureButton: {
    position: 'absolute',
    bottom: 30,
    alignSelf: 'center',
    width: 70,
    height: 70,
    borderRadius: 35,
    backgroundColor: 'rgba(255,255,255,0.3)',
    justifyContent: 'center',
    alignItems: 'center',
  },
  captureButtonDisabled: {
    opacity: 0.5,
  },
  captureButtonInner: {
    width: 60,
    height: 60,
    borderRadius: 30,
    backgroundColor: '#fff',
  },
  previewSection: {
    padding: 20,
  },
  previewHeader: {
    marginBottom: 15,
  },
  previewTitle: {
    fontSize: 18,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 4,
  },
  previewSubtitle: {
    fontSize: 14,
    color: COLORS.gray,
  },
  previewGrid: {
    flexDirection: 'row',
    flexWrap: 'wrap',
    justifyContent: 'space-between',
  },
  previewItem: {
    width: '48%',
    marginBottom: 15,
  },
  previewLabelContainer: {
    flexDirection: 'row',
    alignItems: 'center',
    marginBottom: 5,
    position: 'relative',
  },
  previewLabel: {
    fontSize: 14,
    color: COLORS.gray,
    marginLeft: 5,
  },
  previewLabelCaptured: {
    color: COLORS.success,
    fontWeight: '500',
  },
  currentIndicator: {
    marginLeft: 5,
  },
  previewImageContainer: {
    position: 'relative',
  },
  previewImage: {
    width: '100%',
    height: 100,
    borderRadius: 8,
  },
  previewOverlay: {
    position: 'absolute',
    top: 0,
    left: 0,
    right: 0,
    bottom: 0,
    backgroundColor: 'rgba(0,0,0,0.3)',
    borderRadius: 8,
    justifyContent: 'center',
    alignItems: 'center',
  },
  previewPlaceholder: {
    width: '100%',
    height: 100,
    borderRadius: 8,
    backgroundColor: COLORS.lightGray,
    justifyContent: 'center',
    alignItems: 'center',
  },
  previewPlaceholderText: {
    color: COLORS.gray,
    fontSize: 12,
    marginTop: 5,
  },
  tipsContainer: {
    flexDirection: 'row',
    backgroundColor: 'rgba(255,193,7,0.1)',
    borderRadius: 12,
    padding: 15,
    marginHorizontal: 20,
    marginBottom: 20,
  },
  tipsContent: {
    flex: 1,
    marginLeft: 10,
  },
  tipsTitle: {
    fontSize: 16,
    fontWeight: '600',
    color: COLORS.dark,
    marginBottom: 5,
  },
  tipsText: {
    fontSize: 14,
    color: COLORS.gray,
    lineHeight: 20,
  },
  submitButton: {
    flexDirection: 'row',
    backgroundColor: COLORS.primary,
    borderRadius: 12,
    height: 56,
    justifyContent: 'center',
    alignItems: 'center',
    marginHorizontal: 20,
    marginBottom: 15,
    shadowColor: COLORS.primary,
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 5,
  },
  submitButtonDisabled: {
    backgroundColor: COLORS.lightGray,
  },
  submitButtonText: {
    color: '#fff',
    fontSize: 18,
    fontWeight: '600',
    marginRight: 10,
  },
  securityText: {
    textAlign: 'center',
    fontSize: 12,
    color: COLORS.gray,
    marginBottom: 20,
    paddingHorizontal: 20,
  },
  progressNote: {
    flexDirection: 'row',
    alignItems: 'center',
    justifyContent: 'center',
    marginBottom: 30,
  },
  progressNoteText: {
    marginLeft: 8,
    fontSize: 14,
    color: COLORS.primary,
    fontWeight: '500',
  },
});

export default SignupScreen3;