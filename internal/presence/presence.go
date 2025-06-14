package presence

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fiatjaf/khatru"
)

// onlineMap stores the number of active connections for each pubkey (>=1 means online).
var onlineMap sync.Map // map[string]*int32

// increment increases the counter when a new authenticated connection is established.
func increment(pubkey string) {
	if pubkey == "" {
		return
	}
	cntPtr, _ := onlineMap.LoadOrStore(pubkey, new(int32))
	atomic.AddInt32(cntPtr.(*int32), 1)
}

// decrement decreases the counter on disconnect; deletes the record when it reaches 0.
func decrement(pubkey string) {
	if pubkey == "" {
		return
	}
	if cntPtr, ok := onlineMap.Load(pubkey); ok {
		if atomic.AddInt32(cntPtr.(*int32), -1) <= 0 {
			onlineMap.Delete(pubkey)
		}
	}
}

// IsOnline reports whether the pubkey currently has at least one authenticated connection.
func IsOnline(pubkey string) bool {
	_, ok := onlineMap.Load(pubkey)
	return ok
}

// OnlineCount returns the number of active authenticated connections for the pubkey.
func OnlineCount(pubkey string) int32 {
	if cntPtr, ok := onlineMap.Load(pubkey); ok {
		return atomic.LoadInt32(cntPtr.(*int32))
	}
	return 0
}

// SetupPresenceHooks attaches presence-tracking hooks to the relay.
// Call this once after the relay is initialized and before the HTTP server starts.
func SetupPresenceHooks(relay *khatru.Relay) {
	// On new websocket connection
	relay.OnConnect = append(relay.OnConnect, func(ctx context.Context) {
		ws := khatru.GetConnection(ctx)
		if ws == nil {
			return
		}

		// Wait until the client completes NIP-42 AUTH, then mark as online.
		go func() {
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if pk := ws.AuthedPublicKey; pk != "" {
						increment(pk)
						return
					}
				}
			}
		}()
	})

	// On websocket disconnect
	relay.OnDisconnect = append(relay.OnDisconnect, func(ctx context.Context) {
		ws := khatru.GetConnection(ctx)
		if ws == nil {
			return
		}
		if pk := ws.AuthedPublicKey; pk != "" {
			decrement(pk)
		}
	})
}
