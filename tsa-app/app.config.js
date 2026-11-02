// Build variants on top of app.json.
//
// APP_VARIANT=development builds "TSA Connect Dev": a dev client with its own
// package / bundle id, so it installs next to the store app instead of
// replacing it. google-services.json only registers the store package, so the
// dev variant drops it; push registration then fails softly
// (hooks/usePushRegistration.ts) and push notifications do not arrive there.
//
// Local builds: android/ and ios/ are generated, so switch variants with
// `APP_VARIANT=development npx expo prebuild --clean` before building. iOS
// also needs a provisioning profile for the .dev bundle id.
module.exports = ({ config }) => {
  if (process.env.APP_VARIANT !== 'development') return config;

  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  const { googleServicesFile, ...android } = config.android;
  return {
    ...config,
    name: 'TSA Connect Dev',
    android: { ...android, package: 'com.tsaconnectworld.mobile.dev' },
    ios: { ...config.ios, bundleIdentifier: 'com.mcgp.TsaApp.dev' },
  };
};
