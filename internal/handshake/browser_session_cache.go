package handshake

import (
	"crypto/tls"
	utls "github.com/refraction-networking/utls"
)

// Serialize through the public session-state APIs, preserving the library's
// transport-parameter Extra fields without unsafe access to TLS internals.
type browserSessionCache struct{ cache tls.ClientSessionCache }

func (c browserSessionCache) Get(key string) (*utls.ClientSessionState, bool) {
	cached, ok := c.cache.Get(key)
	if !ok || cached == nil {
		return nil, false
	}
	ticket, state, err := cached.ResumptionState()
	if err != nil {
		return nil, false
	}
	encoded, err := state.Bytes()
	if err != nil {
		return nil, false
	}
	converted, err := utls.ParseSessionState(encoded)
	if err != nil {
		return nil, false
	}
	result, err := utls.NewResumptionState(ticket, converted)
	return result, err == nil
}
func (c browserSessionCache) Put(key string, state *utls.ClientSessionState) {
	if state == nil {
		c.cache.Put(key, nil)
		return
	}
	ticket, session, err := state.ResumptionState()
	if err != nil {
		return
	}
	encoded, err := session.Bytes()
	if err != nil {
		return
	}
	converted, err := tls.ParseSessionState(encoded)
	if err != nil {
		return
	}
	result, err := tls.NewResumptionState(ticket, converted)
	if err == nil {
		c.cache.Put(key, result)
	}
}
