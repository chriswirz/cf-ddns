//go:build live

package publicip

import (
	"context"
	"testing"
	"time"
)

// TestLive hits the real providers. Run with: go test -tags live ./internal/publicip
func TestLive(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ip, err := NewResolver(IPv4).Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("public IPv4: %s", ip)
}
