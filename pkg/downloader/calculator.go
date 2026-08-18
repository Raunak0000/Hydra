package downloader

import "sync/atomic"

type Chunk struct {
	Index int
	Start int64
	End   int64
}

type AdaptiveTracker struct {
	Index       int
	CurrentPtr  int64
	EndBoundary int64
}

func (at *AdaptiveTracker) GetCurrent() int64 {
	return atomic.LoadInt64(&at.CurrentPtr)
}

func (at *AdaptiveTracker) GetEnd() int64 {
	return atomic.LoadInt64(&at.EndBoundary)
}

func CalculateChunks(fileSize int64, numThreads int) []Chunk {
	// If file size is unknown or single-thread fallback, return an open-ended chunk
	if fileSize <= 0 || numThreads <= 1 {
		return []Chunk{
			{
				Index: 0,
				Start: 0,
				End:   fileSize, // 0 or -1 represents open stream
			},
		}
	}

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

func StealWork(trackers []*AdaptiveTracker, dynamicMinChunk int64) (int64, int64, *AdaptiveTracker) {
	var targetTracker *AdaptiveTracker
	var maxRemaining int64 = 0

	for _, tr := range trackers {
		current := atomic.LoadInt64(&tr.CurrentPtr)
		end := atomic.LoadInt64(&tr.EndBoundary)
		if end <= 0 {
			continue // Do not steal from open-ended dynamic streams
		}
		remaining := end - current

		if remaining > maxRemaining {
			maxRemaining = remaining
			targetTracker = tr
		}
	}

	if maxRemaining < dynamicMinChunk*2 {
		return 0, 0, nil
	}

	for {
		current := atomic.LoadInt64(&targetTracker.CurrentPtr)
		end := atomic.LoadInt64(&targetTracker.EndBoundary)
		remaining := end - current

		if remaining < dynamicMinChunk*2 {
			return 0, 0, nil
		}

		midpoint := current + (remaining / 2)

		if atomic.CompareAndSwapInt64(&targetTracker.EndBoundary, end, midpoint) {
			return midpoint, end, targetTracker
		}
	}
}
