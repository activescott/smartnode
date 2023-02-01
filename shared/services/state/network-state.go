package state

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/rocket-pool/rocketpool-go/minipool"
	"github.com/rocket-pool/rocketpool-go/node"
	"github.com/rocket-pool/rocketpool-go/rocketpool"
	"github.com/rocket-pool/smartnode/shared/services/beacon"
	"github.com/rocket-pool/smartnode/shared/services/config"
	cfgtypes "github.com/rocket-pool/smartnode/shared/types/config"
)

type NetworkStateManager struct {
	rp           *rocketpool.RocketPool
	ec           rocketpool.ExecutionClient
	bc           beacon.Client
	Config       *config.RocketPoolConfig
	Network      cfgtypes.Network
	ChainID      uint
	BeaconConfig beacon.Eth2Config
}

type NetworkState struct {
	ElBlockNumber    uint64
	BeaconSlotNumber uint64

	// Insert various state variables as required

	// Node details
	NodeDetails          []node.NativeNodeDetails
	NodeDetailsByAddress map[common.Address]*node.NativeNodeDetails

	// Minipool details
	MinipoolDetails          []minipool.NativeMinipoolDetails
	MinipoolDetailsByAddress map[common.Address]*minipool.NativeMinipoolDetails
	MinipoolDetailsByNode    map[common.Address][]*minipool.NativeMinipoolDetails
}

// Create a new manager for the network state
func NewNetworkStateManager(rp *rocketpool.RocketPool, cfg *config.RocketPoolConfig, ec rocketpool.ExecutionClient, bc beacon.Client) (*NetworkStateManager, error) {

	// Create the manager
	m := &NetworkStateManager{
		rp:      rp,
		ec:      ec,
		bc:      bc,
		Config:  cfg,
		Network: cfg.Smartnode.Network.Value.(cfgtypes.Network),
		ChainID: cfg.Smartnode.GetChainID(),
	}

	// Get the Beacon config info
	var err error
	m.BeaconConfig, err = m.bc.GetEth2Config()
	if err != nil {
		return nil, err
	}

	return m, nil

}

// Get the state of the network at the provided EL block
func (m *NetworkStateManager) GetStateAtElBlock(blockNumber *uint64) (*NetworkState, error) {

	var opts *bind.CallOpts
	if blockNumber == nil {
		opts = nil
	} else {
		opts = &bind.CallOpts{
			BlockNumber: big.NewInt(0).SetUint64(*blockNumber),
		}
	}

	// Create the state wrapper
	state := &NetworkState{
		NodeDetailsByAddress:     map[common.Address]*node.NativeNodeDetails{},
		MinipoolDetailsByAddress: map[common.Address]*minipool.NativeMinipoolDetails{},
		MinipoolDetailsByNode:    map[common.Address][]*minipool.NativeMinipoolDetails{},
	}

	// Node details
	nodeDetails, err := node.GetAllNativeNodeDetails(m.rp, opts)
	if err != nil {
		return nil, err
	}
	state.NodeDetails = nodeDetails

	// Minipool details
	minipoolDetails, err := minipool.GetAllNativeMinipoolDetails(m.rp, opts)
	if err != nil {
		return nil, err
	}

	// Create the node lookup
	for _, details := range nodeDetails {
		state.NodeDetailsByAddress[details.Address] = &details
	}

	// Create the minipool lookups
	for _, details := range minipoolDetails {
		state.MinipoolDetailsByAddress[details.MinipoolAddress] = &details

		// The map of nodes to minipools
		nodeList, exists := state.MinipoolDetailsByNode[details.NodeAddress]
		if !exists {
			nodeList = []*minipool.NativeMinipoolDetails{}
		}
		nodeList = append(nodeList, &details)
		state.MinipoolDetailsByNode[details.NodeAddress] = nodeList
	}

	return state, nil

}

// Get the state of the network at the provided Beacon slot
func (m *NetworkStateManager) GetStateAtBeaconSlot(slotNumber *uint64) (*NetworkState, error) {

	if slotNumber == nil {
		return m.GetStateAtElBlock(nil)
	}

	// Get the execution block for the given slot
	beaconBlock, exists, err := m.bc.GetBeaconBlock(fmt.Sprintf("%d", *slotNumber))
	if err != nil {
		return nil, fmt.Errorf("error getting Beacon block for slot %s: %w", *slotNumber, err)
	}

	if !exists {
		return nil, fmt.Errorf("slot %d did not have a Beacon block", *slotNumber)
	}

	return m.GetStateAtElBlock(&beaconBlock.ExecutionBlockNumber)

}
