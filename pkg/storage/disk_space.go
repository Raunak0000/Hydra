package storage

import (
	"fmt"
	"syscall"
)

type DiskSpaceInfo struct {
	Path           string `json:"path"`
	FreeBytes      uint64 `json:"free_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	RequiredBytes  int64  `json:"required_bytes"`
	EnoughSpace    bool   `json:"enough_space"`
	FreeGB         string `json:"free_gb"`
	RequiredGB     string `json:"required_gb"`
}

func GetDiskSpace(path string, requiredBytes int64) (DiskSpaceInfo, error) {
	resolved, err := ResolvePath(path + "/")
	if err != nil {
		return DiskSpaceInfo{}, fmt.Errorf("invalid disk path: %w", err)
	}

	var stat syscall.Statfs_t

	if err := syscall.Statfs(resolved, &stat); err != nil {
		return DiskSpaceInfo{}, fmt.Errorf(
			"unable to determine disk space for %q: %w",
			resolved,
			err,
		)
	}

	availableBytes := stat.Bavail * uint64(stat.Bsize)
	freeBytes := stat.Bfree * uint64(stat.Bsize)

	enoughSpace := true

	if requiredBytes > 0 {
		enoughSpace = availableBytes >= uint64(requiredBytes)
	}

	return DiskSpaceInfo{
		Path:           resolved,
		FreeBytes:      freeBytes,
		AvailableBytes: availableBytes,
		RequiredBytes:  requiredBytes,
		EnoughSpace:    enoughSpace,
		FreeGB: fmt.Sprintf(
			"%.2f GB",
			float64(availableBytes)/(1024*1024*1024),
		),
		RequiredGB: fmt.Sprintf(
			"%.2f GB",
			float64(requiredBytes)/(1024*1024*1024),
		),
	}, nil
}
