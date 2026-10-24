package main

import (
	"testing"

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
