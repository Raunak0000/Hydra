package main

import (
	"testing"

	"github.com/Raunak0000/Hydra/pkg/downloader"
	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestValidateChunkStates(t *testing.T) {
	tests := []struct {
		name     string
		chunks   []models.ChunkState
		fileSize int64
		wantErr  bool
	}{
		{
			name: "valid incomplete chunk",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: 50},
			},
			fileSize: 100,
		},
		{
			name: "valid completed chunk",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: 100, Completed: true},
			},
			fileSize: 100,
		},
		{
			name:     "empty chunks",
			chunks:   nil,
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "duplicate indexes",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 49, CurrentOffset: 10},
				{Index: 0, Start: 50, End: 99, CurrentOffset: 60},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "offset before start",
			chunks: []models.ChunkState{
				{Index: 0, Start: 10, End: 99, CurrentOffset: 5},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "offset beyond end",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: 101},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "completed chunk with inconsistent offset",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: 50, Completed: true},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "incomplete chunk at end",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: 100},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "chunk end exceeds file size",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 100, CurrentOffset: 50},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "overlapping chunk ranges",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 59, CurrentOffset: 30},
				{Index: 1, Start: 50, End: 99, CurrentOffset: 70},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "gap between chunk ranges",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 39, CurrentOffset: 20},
				{Index: 1, Start: 50, End: 99, CurrentOffset: 70},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "negative chunk index",
			chunks: []models.ChunkState{
				{Index: -1, Start: 0, End: 99, CurrentOffset: 20},
			},
			fileSize: 100,
			wantErr:  true,
		},
		{
			name: "negative current offset",
			chunks: []models.ChunkState{
				{Index: 0, Start: 0, End: 99, CurrentOffset: -1},
			},
			fileSize: 100,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateChunkStates(tt.chunks, tt.fileSize)

			if (err != nil) != tt.wantErr {
				t.Fatalf("validateChunkStates() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveWorkerThreads(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		defThreads   int
		maxThreads   int
		acceptRanges bool
		size         int64
		want         int
	}{
		{
			name:         "workers.dev subdomain caps to 2",
			url:          "https://dark-moon-cd30.wstnjewyeaykmdg.workers.dev/d/abc/file.mkv",
			defThreads:   8,
			maxThreads:   32,
			acceptRanges: true,
			size:         2381299484,
			want:         2,
		},
		{
			name:         "normal CDN uses default threads",
			url:          "https://cdn.example.com/archive.tar.gz",
			defThreads:   8,
			maxThreads:   32,
			acceptRanges: true,
			size:         1024 * 1024,
			want:         8,
		},
		{
			name:         "workers.dev respects explicit lower default",
			url:          "https://proxy.workers.dev/file.zip",
			defThreads:   1,
			maxThreads:   32,
			acceptRanges: true,
			size:         1024,
			want:         1,
		},
		{
			name:         "no range falls back to 1 thread",
			url:          "https://cdn.example.com/stream.bin",
			defThreads:   8,
			maxThreads:   32,
			acceptRanges: false,
			size:         1024,
			want:         1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveWorkerThreads(tt.url, tt.defThreads, tt.maxThreads, tt.acceptRanges, tt.size); got != tt.want {
				t.Fatalf("resolveWorkerThreads() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestSingleThreadChunksPassValidateChunkStates(t *testing.T) {
	const fileSize = int64(10 * 1024 * 1024)
	chunks := downloader.CalculateChunks(fileSize, 1)
	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk, got %d", len(chunks))
	}

	persisted := []models.ChunkState{{
		Index:         chunks[0].Index,
		Start:         chunks[0].Start,
		CurrentOffset: chunks[0].Start,
		End:           chunks[0].End,
	}}
	if err := validateChunkStates(persisted, fileSize); err != nil {
		t.Fatalf("single-thread chunks failed validateChunkStates: %v (chunks=%+v)", err, persisted)
	}
}
