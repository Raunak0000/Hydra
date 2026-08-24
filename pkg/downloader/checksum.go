package downloader

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"strings"
)

type ChecksumResult struct {
	Algorithm string `json:"algorithm"`
	Expected  string `json:"expected"`
	Computed  string `json:"computed"`
	Matched   bool   `json:"matched"`
}

// VerifyFileChecksum calculates the hash of the assembled file and compares it.
func VerifyFileChecksum(filePath string, expectedHash string, algo string) (ChecksumResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return ChecksumResult{}, fmt.Errorf("failed to open file for checksum verification: %w", err)
	}
	defer file.Close()

	normalizedAlgo := strings.ToLower(strings.TrimSpace(algo))
	cleanExpected := strings.ToLower(strings.TrimSpace(expectedHash))

	var computed string

	switch normalizedAlgo {
	case "sha256", "sha-256":
		hasher := sha256.New()
		if _, err := io.Copy(hasher, file); err != nil {
			return ChecksumResult{}, err
		}
		computed = hex.EncodeToString(hasher.Sum(nil))

	case "md5":
		hasher := md5.New()
		if _, err := io.Copy(hasher, file); err != nil {
			return ChecksumResult{}, err
		}
		computed = hex.EncodeToString(hasher.Sum(nil))

	case "crc32":
		table := crc32.MakeTable(crc32.IEEE)
		hasher := crc32.New(table)
		if _, err := io.Copy(hasher, file); err != nil {
			return ChecksumResult{}, err
		}
		computed = fmt.Sprintf("%08x", hasher.Sum32())

	default:
		return ChecksumResult{}, fmt.Errorf("unsupported hashing algorithm: %s", algo)
	}

	matched := strings.EqualFold(computed, cleanExpected)
	return ChecksumResult{
		Algorithm: normalizedAlgo,
		Expected:  cleanExpected,
		Computed:  computed,
		Matched:   matched,
	}, nil
}
