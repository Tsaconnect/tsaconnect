package services

import (
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/accounts/abi"
)

// rewardDistributorABI mirrors the MCGPRewardDistributor surface the backend
// needs to build unsigned txs + decode reads. Keep in sync with
// smart-contracts/contracts/MCGPRewardDistributor.sol — only the symbols the
// backend uses are inlined (setMerkleRoot to build the admin's root-publish tx,
// claim so the app's unsigned-tx shape is documented here, and the
// leafHash/merkleRoot/claimed views for optional on-chain assertions).
const rewardDistributorABI = `[
	{"inputs":[{"name":"root","type":"bytes32"}],"name":"setMerkleRoot","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"account","type":"address"},{"name":"cumulativeAmount","type":"uint256"},{"name":"proof","type":"bytes32[]"}],"name":"claim","outputs":[],"stateMutability":"nonpayable","type":"function"},
	{"inputs":[{"name":"account","type":"address"},{"name":"cumulativeAmount","type":"uint256"}],"name":"leafHash","outputs":[{"type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[],"name":"merkleRoot","outputs":[{"type":"bytes32"}],"stateMutability":"view","type":"function"},
	{"inputs":[{"name":"","type":"address"}],"name":"claimed","outputs":[{"type":"uint256"}],"stateMutability":"view","type":"function"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"oldRoot","type":"bytes32"},{"indexed":true,"name":"newRoot","type":"bytes32"}],"name":"MerkleRootUpdated","type":"event"},
	{"anonymous":false,"inputs":[{"indexed":true,"name":"account","type":"address"},{"indexed":false,"name":"amount","type":"uint256"},{"indexed":false,"name":"cumulativeAmount","type":"uint256"}],"name":"Claimed","type":"event"}
]`

var parsedRewardDistributorABI abi.ABI

func init() {
	var err error
	parsedRewardDistributorABI, err = abi.JSON(strings.NewReader(rewardDistributorABI))
	if err != nil {
		panic(fmt.Sprintf("failed to parse MCGPRewardDistributor ABI: %v", err))
	}
}
