package watchtower

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/klauspost/compress/zstd"
	"github.com/rocket-pool/rocketpool-go/rocketpool"
	"github.com/rocket-pool/smartnode/shared/services/beacon"
	"github.com/rocket-pool/smartnode/shared/services/config"
	"github.com/rocket-pool/smartnode/shared/services/rewards"
	"github.com/rocket-pool/smartnode/shared/services/state"
	"github.com/rocket-pool/smartnode/shared/utils/log"
)

const (
	recordsFilenameFormat  string = "%d-%d.json.zst"
	recordsFilenamePattern string = "(?P<slot>\\d+)\\-(?P<epoch>\\d+)\\.json\\.zst"
	checksumTableFilename  string = "checksums.sha384"
)

// Manager for RollingRecords
type RollingRecordManager struct {
	Record                       *rewards.RollingRecord
	LatestFinalizedEpoch         uint64
	ExpectedBalancesBlock        uint64
	ExpectedRewardsIntervalBlock uint64

	log                  log.ColorLogger
	logPrefix            string
	cfg                  *config.RocketPoolConfig
	rp                   *rocketpool.RocketPool
	bc                   beacon.Client
	mgr                  *state.NetworkStateManager
	nodeAddress          common.Address
	startSlot            uint64
	beaconCfg            beacon.Eth2Config
	genesisTime          time.Time
	compressor           *zstd.Encoder
	decompressor         *zstd.Decoder
	recordsFilenameRegex *regexp.Regexp
}

// Creates a new manager for rolling records.
func NewRollingRecordManager(log log.ColorLogger, logPrefix string, cfg *config.RocketPoolConfig, rp *rocketpool.RocketPool, bc beacon.Client, mgr *state.NetworkStateManager, nodeAddress common.Address, startSlot uint64, beaconConfig *beacon.Eth2Config) (*RollingRecordManager, error) {
	// Get the beacon config and the genesis time
	beaconCfg, err := bc.GetEth2Config()
	if err != nil {
		return nil, fmt.Errorf("error getting beacon config: %w", err)
	}
	genesisTime := time.Unix(int64(beaconCfg.GenesisTime), 0)

	// Create the zstd compressor and decompressor
	encoder, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		return nil, fmt.Errorf("error creating zstd compressor for rolling record manager: %w", err)
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, fmt.Errorf("error creating zstd decompressor for rolling record manager: %w", err)
	}

	// Create the records filename regex
	recordsFilenameRegex := regexp.MustCompile(recordsFilenamePattern)

	// Make the records folder if it doesn't exist
	recordsPath := cfg.Smartnode.GetRecordsPath()
	fileInfo, err := os.Stat(recordsPath)
	if os.IsNotExist(err) {
		err = os.Mkdir(recordsPath, 0644)
		if err != nil {
			return nil, fmt.Errorf("error creating rolling records folder: %w", err)
		}
	}
	if !fileInfo.IsDir() {
		return nil, fmt.Errorf("rolling records folder location exists (%s), but is not a folder", recordsPath)
	}

	return &RollingRecordManager{
		Record: rewards.NewRollingRecord(log, logPrefix, bc, startSlot, beaconConfig),

		log:                  log,
		logPrefix:            logPrefix,
		cfg:                  cfg,
		rp:                   rp,
		bc:                   bc,
		mgr:                  mgr,
		nodeAddress:          nodeAddress,
		startSlot:            startSlot,
		beaconCfg:            beaconCfg,
		genesisTime:          genesisTime,
		compressor:           encoder,
		decompressor:         decoder,
		recordsFilenameRegex: recordsFilenameRegex,
	}, nil
}

// Process the details of the latest head state
func (r *RollingRecordManager) ProcessNewHeadState(state *state.NetworkState) error {

	// Get the latest finalized slot
	latestFinalizedBlock, err := r.mgr.GetLatestFinalizedBeaconBlock()
	if err != nil {
		return fmt.Errorf("error getting latest finalized block: %w", err)
	}

	// Get the epoch that slot is for
	finalizedEpoch := latestFinalizedBlock.Slot / state.BeaconConfig.SlotsPerEpoch

	// Check if a network balance update is due
	isNetworkBalanceUpdateDue, networkBalanceSlot, err := r.isNetworkBalanceUpdateRequired(state)
	if err != nil {
		return fmt.Errorf("error checking if network balance update is required: %w", err)
	}

	// Check if a rewards interval is due
	isRewardsSubmissionDue, rewardsSlot, err := r.isRewardsIntervalSubmissionRequired(state)
	if err != nil {
		return fmt.Errorf("error checking if rewards submission is required: %w", err)
	}

	if !isNetworkBalanceUpdateDue && !isRewardsSubmissionDue {
		// No special upcoming state required, so update normally
		_, err = r.updateToSlot(latestFinalizedBlock.Slot)
		if err != nil {
			return fmt.Errorf("error during previous rolling record update: %w", err)
		}
		return nil
	}

	var earliestRequiredSlot uint64
	if isNetworkBalanceUpdateDue && isRewardsSubmissionDue {
		if networkBalanceSlot < rewardsSlot {
			earliestRequiredSlot = networkBalanceSlot
		} else {
			earliestRequiredSlot = rewardsSlot
		}
	} else if isNetworkBalanceUpdateDue {
		earliestRequiredSlot = networkBalanceSlot
	} else if isRewardsSubmissionDue {
		earliestRequiredSlot = rewardsSlot
	}

	// Do NOT do an update until the required slot has been finalized
	if latestFinalizedBlock.Slot < earliestRequiredSlot {
		r.log.Printlnf("%s TODO: Message goes here")
	}
}

// Start an update to a given slot
func (r *RollingRecordManager) updateToSlot(slot uint64) (bool, error) {
	// Skip it if the latest record is already up to date or is in the process
	if r.Record.PendingSlot >= slot {
		return false, nil
	}

	// Get the latest finalized state
	state, err := r.mgr.GetStateForSlot(slot)
	if err != nil {
		return false, fmt.Errorf("error getting state at slot %d: %w", slot, err)
	}

	// Run an update on it
	updateStarted, err := r.Record.UpdateToState(state, false)
	if err != nil {
		return false, fmt.Errorf("error updating rolling record to slot %d, block %d: %w", state.BeaconSlotNumber, state.ElBlockNumber, err)
	}

	return updateStarted, nil
}

// Check if a network balance submission is required and if so, the slot number for the update
func (r *RollingRecordManager) isNetworkBalanceUpdateRequired(state *state.NetworkState) (bool, uint64, error) {
	// Get block to submit balances for
	blockNumberBig := state.NetworkDetails.LatestReportableBalancesBlock
	blockNumber := blockNumberBig.Uint64()

	// Check if a submission needs to be made
	if blockNumber <= state.NetworkDetails.BalancesBlock.Uint64() {
		return false, 0, nil
	}

	// Check if this node has already submitted a balance
	blockNumberBuf := make([]byte, 32)
	big.NewInt(int64(blockNumber)).FillBytes(blockNumberBuf)
	hasSubmitted, err := r.rp.RocketStorage.GetBool(nil, crypto.Keccak256Hash([]byte("network.balances.submitted.node"), r.nodeAddress.Bytes(), blockNumberBuf))
	if err != nil {
		return false, 0, fmt.Errorf("error checking if node has already submitted network balance for block %d: %w", blockNumber, err)
	}
	if hasSubmitted {
		return false, 0, nil
	}

	// Get the time of the block
	header, err := r.rp.Client.HeaderByNumber(context.Background(), big.NewInt(0).SetUint64(blockNumber))
	if err != nil {
		return false, 0, fmt.Errorf("error getting header for block %d: %w", blockNumber, err)
	}
	blockTime := time.Unix(int64(header.Time), 0)

	// Get the Beacon block corresponding to this time
	eth2Config := state.BeaconConfig
	timeSinceGenesis := blockTime.Sub(r.genesisTime)
	slotNumber := uint64(timeSinceGenesis.Seconds()) / eth2Config.SecondsPerSlot
	return true, slotNumber, nil
}

// Check if a rewards interval submission is required and if so, the slot number for the update
func (r *RollingRecordManager) isRewardsIntervalSubmissionRequired(state *state.NetworkState) (bool, uint64, error) {
	// Check if a rewards interval has passed and needs to be calculated
	startTime := state.NetworkDetails.IntervalStart
	intervalTime := state.NetworkDetails.IntervalDuration

	// Calculate the end time, which is the number of intervals that have gone by since the current one's start
	secondsSinceGenesis := time.Duration(state.BeaconConfig.SecondsPerSlot*state.BeaconSlotNumber) * time.Second
	stateTime := r.genesisTime.Add(secondsSinceGenesis)
	timeSinceStart := stateTime.Sub(startTime)
	intervalsPassed := timeSinceStart / intervalTime
	endTime := startTime.Add(intervalTime * intervalsPassed)
	if intervalsPassed == 0 {
		return false, 0, nil
	}

	// Check if this node already submitted a tree
	currentIndex := state.NetworkDetails.RewardIndex
	currentIndexBig := big.NewInt(0).SetUint64(currentIndex)
	indexBuffer := make([]byte, 32)
	currentIndexBig.FillBytes(indexBuffer)
	hasSubmitted, err := r.rp.RocketStorage.GetBool(nil, crypto.Keccak256Hash([]byte("rewards.snapshot.submitted.node"), r.nodeAddress.Bytes(), indexBuffer))
	if err != nil {
		return false, 0, fmt.Errorf("error checking if node has already submitted for rewards interval %d: %w", currentIndex, err)
	}
	if hasSubmitted {
		return false, 0, nil
	}

	// Get the target slot number
	eth2Config := state.BeaconConfig
	totalTimespan := endTime.Sub(r.genesisTime)
	targetSlot := uint64(math.Ceil(totalTimespan.Seconds() / float64(eth2Config.SecondsPerSlot)))
	targetSlotEpoch := targetSlot / eth2Config.SlotsPerEpoch
	targetSlot = targetSlotEpoch*eth2Config.SlotsPerEpoch + (eth2Config.SlotsPerEpoch - 1) // The target slot becomes the last one in the Epoch

	return true, targetSlot, nil
}

// Generate a new record for the provided slot using the latest viable saved record
func (r *RollingRecordManager) generateRecordForSlot(slot uint64) (*rewards.RollingRecord, error) {

	// Create a state for the target slot
	state, err := r.mgr.GetStateForSlot(slot)
	if err != nil {
		return nil, fmt.Errorf("error generating state for slot %d: %w", slot, err)
	}

	// Load the latest viable record
	record, err := r.loadRecordFromDisk(r.startSlot, slot)
	if err != nil {
		return nil, fmt.Errorf("error loading best record for slot %d: %w", slot, err)
	}

	// Update the record to the target state
	err = record.UpdateToState(state)
	if err != nil {
		return nil, fmt.Errorf("error updating record to slot %d: %w", slot, err)
	}

	return record, nil

}

// Save the rolling record to a file and update the record info catalog
func (r *RollingRecordManager) saveRecordToFile() error {

	// Serialize the record
	bytes, err := r.Record.Serialize()
	if err != nil {
		return fmt.Errorf("error saving rolling record: %w", err)
	}

	// Compress the record
	compressedBytes := r.compressor.EncodeAll(bytes, make([]byte, 0, len(bytes)))

	// Get the record filename
	slot := r.Record.LastProcessedSlot
	epoch := r.Record.LastProcessedSlot / r.beaconCfg.SlotsPerEpoch
	recordsPath := r.cfg.Smartnode.GetRecordsPath()
	filename := filepath.Join(recordsPath, fmt.Sprintf(recordsFilenameFormat, slot, epoch))

	// Write it to a file
	err = os.WriteFile(filename, compressedBytes, 0664)
	if err != nil {
		return fmt.Errorf("error writing file [%s]: %w", filename, err)
	}

	// Compute the SHA384 hash to act as a checksum
	checksum := sha512.Sum384(compressedBytes)

	// Load the existing checksum table
	checksumFilename := filepath.Join(recordsPath, checksumTableFilename)
	checksumFile, err := os.OpenFile(checksumFilename, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("error opening checksum file: %w", err)
	}
	defer checksumFile.Close()

	// Append and write the new checksum out to file
	checksumLine := fmt.Sprintf("%s  %s\n", hex.EncodeToString(checksum[:]), filename)
	checksumFile.WriteString(checksumLine)

	return nil
}

// Load the most recent appropriate rolling record from disk, using the checksum table as an index
func (r *RollingRecordManager) loadRecordFromDisk(startSlot uint64, targetSlot uint64) (*rewards.RollingRecord, error) {

	// Open the checksum file
	recordsPath := r.cfg.Smartnode.GetRecordsPath()
	checksumFilename := filepath.Join(recordsPath, checksumTableFilename)
	checksumTable, err := os.ReadFile(checksumFilename)
	if err != nil {
		return nil, fmt.Errorf("error loading checksum table (%s): %w", checksumFilename, err)
	}

	// Parse out each line
	lines := strings.Split(string(checksumTable), "\n")

	// Iterate over each file, counting backwards from the bottom
	for i := len(lines) - 1; i >= 0; i++ {
		line := lines[i]
		if line == "" {
			// Ignore blank lines
			continue
		}

		// Extract the checksum and filename
		elems := strings.Split(line, "  ")
		if len(elems) != 2 {
			return nil, fmt.Errorf("error parsing checkpoint line (%s): expected 2 elements, but got %d", line, len(elems))
		}
		checksumString := elems[0]
		filename := elems[1]

		// Extract the slot number for this file
		slot, err := r.getSlotFromFilename(filename)
		if err != nil {
			return nil, fmt.Errorf("error scanning checkpoint line (%s): %w", line, err)
		}

		// Check if the slot was too far into the future
		if slot > targetSlot || slot > startSlot {
			continue
		}

		// Make sure the checksum parses properly
		checksum, err := hex.DecodeString(checksumString)
		if err != nil {
			return nil, fmt.Errorf("error scanning checkpoint line (%s): checksum (%s) could not be parsed", line, checksumString)
		}

		// Try to load it
		record, err := r.loadRecordFromFile(filename, checksum)
		if err != nil {
			return nil, fmt.Errorf("error loading record from file (%s): %w", filename, err)
		}
		return record, nil

	}

	// If we got here then none of the saved files worked so we have to make a new record
	return rewards.NewRollingRecord(r.log, r.logPrefix, r.bc, startSlot, &r.beaconCfg), nil

	// NOTE: should we clear the checksum file here and delete the old records?

}

// Get the slot number from a record filename
func (r *RollingRecordManager) getSlotFromFilename(filename string) (uint64, error) {
	matches := r.recordsFilenameRegex.FindStringSubmatch(filename)
	if matches == nil {
		return 0, fmt.Errorf("filename (%s) did not match the expected format", filename)
	}
	slotIndex := r.recordsFilenameRegex.SubexpIndex("slot")
	if slotIndex == -1 {
		return 0, fmt.Errorf("slot number not found in filename (%s)", filename)
	}
	slotString := matches[slotIndex]
	slot, err := strconv.ParseUint(slotString, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("slot (%s) could not be parsed to a number")
	}

	return slot, nil
}

// Load a record from a file, making sure its contents match the provided checksum
func (r *RollingRecordManager) loadRecordFromFile(filename string, expectedChecksum []byte) (*rewards.RollingRecord, error) {
	// Read the file
	compressedBytes, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("error reading file: %w", filename, err)
	}

	// Calculate the hash and validate it
	checksum := sha512.Sum384(compressedBytes)
	if !bytes.Equal(expectedChecksum, checksum[:]) {
		expectedString := hex.EncodeToString(expectedChecksum)
		actualString := hex.EncodeToString(checksum[:])
		return nil, fmt.Errorf("checksum mismatch (expected %s, but it was %s)", filename, expectedString, actualString)
	}

	// Decompress it
	bytes, err := r.decompressor.DecodeAll(compressedBytes, []byte{})
	if err != nil {
		return nil, fmt.Errorf("error decompressing data: %w", err)
	}

	// Create a new record from the data
	return rewards.DeserializeRollingRecord(r.log, r.logPrefix, r.bc, &r.beaconCfg, bytes)
}