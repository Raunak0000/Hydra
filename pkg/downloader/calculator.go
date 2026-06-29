package downloader

import "sync/atomic"

type Chunk struct {
	Index int
	Start int64
	End   int64
}

type AdaptiveTracker struct {
	Index       int
	CurrentPtr  int64 // Atomic pointer tracking current write offset position
	EndBoundary int64 // Atomic pointer tracking the dynamic end ceiling
}

func (at *AdaptiveTracker) GetCurrent() int64 {
	return atomic.LoadInt64(&at.CurrentPtr)
}

func (at *AdaptiveTracker) GetEnd() int64 {
	return atomic.LoadInt64(&at.EndBoundary)
}

func CalculateChunks(fileSize int64, numThreads int) []Chunk {
	var chunks []Chunk
	chunkSize := fileSize / int64(numThreads)

	for i := 0; i < numThreads; i++ {
		startByte := int64(i) * chunkSize
		var endByte int64

		if i == numThreads-1 {
			endByte = fileSize - 1
		} else {
			endByte = startByte + chunkSize - 1
		}

		chunks = append(chunks, Chunk{
			Index: i,
			Start: startByte,
			End:   endByte,
		})
	}

	return chunks
}

// StealWork searches active trackers to find the heaviest remaining load and splits it
func StealWork(trackers []*AdaptiveTracker, dynamicMinChunk int64) (int64, int64, *AdaptiveTracker) {
	var targetTracker *AdaptiveTracker
	var maxRemaining int64 = 0

	// 1. Scan all active trackers to find the worker with the most work left
	for _, tr := range trackers {
		current := atomic.LoadInt64(&tr.CurrentPtr)
		end := atomic.LoadInt64(&tr.EndBoundary)
		remaining := end - current

		if remaining > maxRemaining {
			maxRemaining = remaining
			targetTracker = tr
		}
	}

	// 2. Safeguard: Only steal if the remaining work is worth the scheduling overhead
	if maxRemaining < dynamicMinChunk*2 {
		return 0, 0, nil
	}

	// 3. Thread-safely split the remaining workload in half
	for {
		current := atomic.LoadInt64(&targetTracker.CurrentPtr)
		end := atomic.LoadInt64(&targetTracker.EndBoundary)
		remaining := end - current
		
		if remaining < dynamicMinChunk*2 {
			return 0, 0, nil
		}

		// Calculate the new midpoint split boundary
		midpoint := current + (remaining / 2)

		// Atomically attempt to pull back the target tracker's end boundary to the midpoint
		if atomic.CompareAndSwapInt64(&targetTracker.EndBoundary, end, midpoint) {
			// Success! We stole the upper half: from midpoint to the original end
			return midpoint, end, targetTracker
		}
		// If CompareAndSwap failed, it means the target thread advanced or another thread stole it first; retry loop.
	}
}
