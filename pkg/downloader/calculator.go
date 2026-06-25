package downloader

type Chunk struct {
	Index int
	Start int64
	End   int64
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
