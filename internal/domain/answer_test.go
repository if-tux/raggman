package domain

import (
	"slices"
	"testing"
)

func TestCitedMarkers(t *testing.T) {
	tests := []struct {
		answer string
		want   []int
		chunks int
	}{
		{answer: "Raggman answers Questions [1].", chunks: 3, want: []int{1}},
		{answer: "First [2], then [1], again [2].", chunks: 3, want: []int{2, 1}},
		{answer: "Both agree [1, 3].", chunks: 3, want: []int{1, 3}},
		{answer: "Adjacent [1][2].", chunks: 2, want: []int{1, 2}},
		{answer: "Full-width 【2】.", chunks: 2, want: []int{2}},
		{answer: "Out of range [0] and [4].", chunks: 3, want: nil},
		{answer: "No markers, or code like a[i].", chunks: 3, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.answer, func(t *testing.T) {
			if got := CitedMarkers(tt.answer, tt.chunks); !slices.Equal(got, tt.want) {
				t.Fatalf("CitedMarkers(%q, %d) = %v, want %v", tt.answer, tt.chunks, got, tt.want)
			}
		})
	}
}
