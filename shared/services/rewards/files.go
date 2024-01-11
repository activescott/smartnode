package rewards

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ipfs/go-cid"
	"github.com/klauspost/compress/zstd"
	"github.com/rocket-pool/smartnode/shared/services/config"
)

type ILocalFile interface {
	Serialize() ([]byte, error)
}

// A wrapper around ILocalFile representing a local rewards file or minipool performance file.
// Can be used with anything that can be serialzed to bytes or parsed from bytes.
type LocalFile[T ILocalFile] struct {
	f        T
	fullPath string
}

type LocalRewardsFile = LocalFile[IRewardsFile]
type LocalMinipoolPerformanceFile = LocalFile[IMinipoolPerformanceFile]

// NewLocalFile creates the wrapper, but doesn't write to disk.
// This should be used when generating new trees / performance files.
func NewLocalFile[T ILocalFile](ilf T, fullpath string) *LocalFile[T] {
	return &LocalFile[T]{
		f:        ilf,
		fullPath: fullpath,
	}
}

// Reads an existing RewardsFile from disk and wraps it in a LocalFile
func ReadLocalRewardsFile(path string) (*LocalFile[IRewardsFile], error) {
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Error reading rewards file from %s: %w", path, err)
	}

	// Unmarshal it
	proofWrapper, err := DeserializeRewardsFile(fileBytes)
	if err != nil {
		return nil, fmt.Errorf("Error unmarshaling rewards file from %s: %w", path, err)
	}

	return NewLocalFile[IRewardsFile](proofWrapper, path), nil
}

// Reads an existing MinipoolPerformanceFile from disk and wraps it in a LocalFile
func ReadLocalMinipoolPerformanceFile(path string) (*LocalFile[IMinipoolPerformanceFile], error) {
	fileBytes, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("Error reading rewards file from %s: %w", path, err)
	}

	// Unmarshal it
	minipoolPerformance, err := DeserializeMinipoolPerformanceFile(fileBytes)
	if err != nil {
		return nil, fmt.Errorf("Error unmarshaling rewards file from %s: %w", path, err)
	}

	return NewLocalFile[IMinipoolPerformanceFile](minipoolPerformance, path), nil
}

// Returns the underlying interface, IRewardsFile for rewards file, IMinipoolPerformanceFile for performance, etc.
func (lf *LocalFile[T]) Repr() T {
	return lf.f
}

// Converts the underlying interface to a byte slice
func (lf *LocalFile[T]) Serialize() ([]byte, error) {
	return lf.f.Serialize()
}

// Writes the file to disk
func (lf *LocalFile[T]) Write() error {
	data, err := lf.Serialize()
	if err != nil {
		return fmt.Errorf("Error serializing file: %w", err)
	}

	err = os.WriteFile(lf.fullPath, data, 0644)
	if err != nil {
		return fmt.Errorf("Error writing file to %s: %w", lf.fullPath, err)
	}
	return nil
}

// Computes the CID that would be used if we compressed the file with zst,
// added the ipfs extension to the filename (.zst), and uploaded it to ipfs
// in an empty directory, as web3storage did, once upon a time.
//
// N.B. This function will also save the compressed file to disk so it can
// later be uploaded to ipfs
func (lf *LocalFile[T]) CompressedCid() (cid.Cid, error) {
	// Serialize
	data, err := lf.Serialize()
	if err != nil {
		return cid.Cid{}, fmt.Errorf("Error serializing file: %w", err)
	}

	// Compress
	encoder, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	compressedBytes := encoder.EncodeAll(data, make([]byte, 0, len(data)))

	filename := lf.fullPath + config.RewardsTreeIpfsExtension
	c, err := singleFileDirIPFSCid(compressedBytes, filepath.Base(filename))
	if err != nil {
		return cid.Cid{}, fmt.Errorf("Error calculating CID: %w", err)
	}

	// Write to disk
	// Take care to write to `filename` since it has the .zst extension added
	err = os.WriteFile(filename, compressedBytes, 0644)
	if err != nil {
		return cid.Cid{}, fmt.Errorf("Error writing file to %s: %w", lf.fullPath, err)
	}
	return c, nil
}
