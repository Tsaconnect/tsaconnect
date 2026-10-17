// smart-contracts/deploy/create2.js
//
// CREATE2 through the standard deterministic deployment proxy (Arachnid,
// https://github.com/Arachnid/deterministic-deployment-proxy). The factory
// lives at the same address on every EVM chain that has had its presigned,
// chain-agnostic (pre-EIP-155) deployment tx broadcast, so
//   address = keccak256(0xff ++ factory ++ salt ++ keccak256(initCode))[12:]
// is the same everywhere for the same salt and init code.
//
// Calldata to the factory is `salt (32 bytes) ++ initCode`; it returns the new
// address and reverts if the CREATE2 fails (e.g. the address already has code).
const fs = require("fs");
const path = require("path");
const { getAddress, getCreate2Address, keccak256, toUtf8Bytes, concat } = require("ethers");

const FACTORY = "0x4e59b44847b379578588920ca78fbf26c0b4956c";
// The factory's one-time deployer and its presigned deployment tx (100k gas at
// 100 gwei, so the deployer needs exactly 0.01 native to broadcast it).
const FACTORY_DEPLOYER = "0x3fab184622dc19b6109349b94811493bf2a45362";
const FACTORY_DEPLOY_TX =
  "0xf8a58085174876e800830186a08080b853604580600e600039806000f350fe7fffffffffffffffffffffffffffffff" +
  "ffffffffffffffffffffffffffffffffe03601600081602082378035828234f58015156039578182fd5b8082525050" +
  "506014600cf31ba02222222222222222222222222222222222222222222222222222222222222222a0222222222222" +
  "2222222222222222222222222222222222222222222222222222";
const FACTORY_DEPLOY_COST = 10n ** 16n;

const MANIFEST_PATH = path.join(__dirname, "deterministic.json");

const saltOf = (label) => keccak256(toUtf8Bytes(label));
const predict = (salt, initCode) => getCreate2Address(getAddress(FACTORY), salt, keccak256(initCode));

const isLocalNetwork = (hre) => hre.network.name === "hardhat" || hre.network.name === "localhost";

async function hasCode(provider, address) {
  return (await provider.getCode(address)) !== "0x";
}

// Public RPCs are load-balanced: right after a mined deploy, getCode can be
// answered by a node a block behind (seen on BSC testnet 2026-10-17). Poll
// before calling a fresh deploy missing.
const CODE_POLL = { attempts: 20, intervalMs: 1500 };
async function hasCodeAfterDeploy(provider, address, poll = CODE_POLL) {
  for (let i = 0; i < poll.attempts; i++) {
    if (await hasCode(provider, address)) return true;
    if (i + 1 < poll.attempts) await new Promise((r) => setTimeout(r, poll.intervalMs));
  }
  return false;
}

// ensureFactory fails clearly when the factory is missing on a live network. On
// a local hardhat network it installs the REAL factory by broadcasting the same
// presigned tx every live chain used, so tests exercise the genuine bytecode.
async function ensureFactory(hre) {
  const provider = hre.ethers.provider;
  if (await hasCode(provider, FACTORY)) return;
  if (!isLocalNetwork(hre)) {
    throw new Error(
      `create2: the deterministic deployment proxy ${FACTORY} has no code on ${hre.network.name}. ` +
      `Fund ${FACTORY_DEPLOYER} with 0.01 native and broadcast its presigned tx ` +
      "(github.com/Arachnid/deterministic-deployment-proxy) first — or pick another network."
    );
  }
  await provider.send("hardhat_setBalance", [FACTORY_DEPLOYER, "0x" + FACTORY_DEPLOY_COST.toString(16)]);
  const hash = await provider.send("eth_sendRawTransaction", [FACTORY_DEPLOY_TX]);
  await provider.getTransactionReceipt(hash); // local networks automine
  if (!(await hasCode(provider, FACTORY))) throw new Error("create2: presigned factory deployment left no code");
}

// deployCreate2 lands `initCode` at its predicted address, or reuses whatever is
// already there. A front-runner who submits the same deployment first only
// pays our gas: the bytes at the address are the bytes we would have deployed,
// so a reverted tx followed by code at the address counts as success.
async function deployCreate2(hre, signer, { salt, initCode, label }) {
  const provider = hre.ethers.provider;
  const address = predict(salt, initCode);
  if (await hasCode(provider, address)) return { address, reused: true };
  try {
    const tx = await signer.sendTransaction({ to: FACTORY, data: concat([salt, initCode]) });
    await tx.wait();
  } catch (e) {
    if (!(await hasCodeAfterDeploy(provider, address))) throw e;
    return { address, reused: true };
  }
  if (!(await hasCodeAfterDeploy(provider, address))) throw new Error(`create2: ${label} has no code at ${address} after deploy`);
  return { address, reused: false };
}

function readManifest(file = MANIFEST_PATH) {
  if (!fs.existsSync(file)) return {};
  try {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  } catch (e) {
    throw new Error(`create2: manifest ${file} is corrupt (${e.message}) — restore it from git`);
  }
}

function writeManifest(manifest, file = MANIFEST_PATH) {
  fs.writeFileSync(file, JSON.stringify(manifest, null, 2) + "\n");
}

module.exports = {
  FACTORY,
  FACTORY_DEPLOYER,
  MANIFEST_PATH,
  saltOf,
  predict,
  hasCode,
  hasCodeAfterDeploy,
  CODE_POLL,
  isLocalNetwork,
  ensureFactory,
  deployCreate2,
  readManifest,
  writeManifest,
};
