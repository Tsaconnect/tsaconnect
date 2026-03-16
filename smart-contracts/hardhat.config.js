require("@nomicfoundation/hardhat-toolbox");
require("dotenv").config();

/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    version: "0.8.20",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200,
      },
    },
  },
  networks: {
    sonicTestnet: {
      url: process.env.SONIC_TESTNET_RPC || "https://rpc.testnet.soniclabs.com",
      chainId: 14601,
      accounts: process.env.DEPLOYER_PRIVATE_KEY ? [process.env.DEPLOYER_PRIVATE_KEY] : [],
    },
    sonicMainnet: {
      url: process.env.SONIC_MAINNET_RPC || "https://rpc.soniclabs.com",
      chainId: 146,
      accounts: process.env.DEPLOYER_PRIVATE_KEY ? [process.env.DEPLOYER_PRIVATE_KEY] : [],
    },
  },
};
