require("@nomicfoundation/hardhat-toolbox");
require("@openzeppelin/hardhat-upgrades");
require("dotenv").config();

// Accept the deployer key with or without a leading 0x (hardhat needs 0x-prefixed).
const RAW_PK = process.env.DEPLOYER_PRIVATE_KEY;
const ACCOUNTS = RAW_PK ? [RAW_PK.startsWith("0x") ? RAW_PK : "0x" + RAW_PK] : [];

/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    version: "0.8.24",
    settings: {
      optimizer: {
        enabled: true,
        runs: 200,
      },
      // OpenZeppelin v5 deps (used by P2PEscrowPool) emit Cancun opcodes
      // (mcopy/tstore). Sonic's EVM supports Cancun on both mainnet and
      // testnet, so this is safe.
      evmVersion: "cancun",
    },
  },
  networks: {
    sonicTestnet: {
      url: process.env.SONIC_TESTNET_RPC || "https://rpc.testnet.soniclabs.com",
      chainId: 14601,
      accounts: ACCOUNTS,
    },
    sonicMainnet: {
      url: process.env.SONIC_MAINNET_RPC || "https://rpc.soniclabs.com",
      chainId: 146,
      accounts: ACCOUNTS,
    },
    bscTestnet: {
      url: process.env.BSC_TESTNET_RPC || "https://data-seed-prebsc-1-s1.binance.org:8545",
      chainId: 97,
      accounts: ACCOUNTS,
    },
    bscMainnet: {
      url: process.env.BSC_MAINNET_RPC || "https://bsc-dataseed.binance.org",
      chainId: 56,
      accounts: ACCOUNTS,
    },
  },
  etherscan: {
    apiKey: {
      sonicMainnet: process.env.SONICSCAN_API_KEY || "",
      sonicTestnet: process.env.SONICSCAN_API_KEY || "",
      bsc: process.env.BSCSCAN_API_KEY || "",
      bscTestnet: process.env.BSCSCAN_API_KEY || "",
    },
    customChains: [
      {
        network: "sonicMainnet",
        chainId: 146,
        urls: {
          apiURL: "https://api.sonicscan.org/api",
          browserURL: "https://sonicscan.org",
        },
      },
      {
        network: "sonicTestnet",
        chainId: 14601,
        urls: {
          apiURL: "https://api-testnet.sonicscan.org/api",
          browserURL: "https://testnet.sonicscan.org",
        },
      },
      {
        network: "bscTestnet",
        chainId: 97,
        urls: {
          apiURL: "https://api-testnet.bscscan.com/api",
          browserURL: "https://testnet.bscscan.com",
        },
      },
    ],
  },
  sourcify: {
    enabled: false,
  },
};
