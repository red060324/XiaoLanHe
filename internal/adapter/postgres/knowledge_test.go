package postgres

import "testing"

func TestVectorLiteral(t *testing.T) {
	if got := vectorLiteral([]float32{1, -2.5, 0}); got != "[1,-2.5,0]" {
		t.Fatalf("literal=%q", got)
	}
}
