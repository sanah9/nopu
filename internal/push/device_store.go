package push

import (
	"sync"
	"time"
)

// DeviceRegistration holds everything needed to reach a registered device.
type DeviceRegistration struct {
	ID         string    // server-assigned UUID → deviceRegistrationId
	Pubkey     string    // user's Nostr pubkey
	Platform   string    // "ios", "android"
	TokenType  string    // "apns_voip", "unifiedpush"
	Token      string    // APNs hex token OR UnifiedPush endpoint URL
	DeviceID   string    // client-generated stable UUID per device
	AppVersion string
	CreatedAt  time.Time
}

// DeviceStore is a thread-safe in-memory registry.
// It maintains two indexes:
//   - id      → *DeviceRegistration   (primary)
//   - deviceID → id                   (for idempotent re-registration)
type DeviceStore struct {
	mu       sync.Mutex
	byID     map[string]*DeviceRegistration
	byDevice map[string]string // deviceID → registration ID
}

func newDeviceStore() *DeviceStore {
	return &DeviceStore{
		byID:     make(map[string]*DeviceRegistration),
		byDevice: make(map[string]string),
	}
}

// Upsert saves the registration, replacing any previous entry for the same
// DeviceID.  Returns the (possibly new) registration ID.
func (s *DeviceStore) Upsert(reg *DeviceRegistration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Remove stale entry for this device if it exists.
	if oldID, ok := s.byDevice[reg.DeviceID]; ok && oldID != reg.ID {
		delete(s.byID, oldID)
	}
	s.byID[reg.ID] = reg
	if reg.DeviceID != "" {
		s.byDevice[reg.DeviceID] = reg.ID
	}
}

// Get looks up a registration by its server-assigned ID.
func (s *DeviceStore) Get(id string) (*DeviceRegistration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	reg, ok := s.byID[id]
	return reg, ok
}

// Delete removes a registration by its server-assigned ID.
func (s *DeviceStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if reg, ok := s.byID[id]; ok {
		delete(s.byDevice, reg.DeviceID)
		delete(s.byID, id)
	}
}
