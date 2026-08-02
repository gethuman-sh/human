package devcontainer

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/stretchr/testify/assert"
)

func TestFirstContainerIP(t *testing.T) {
	t.Run("nil settings", func(t *testing.T) {
		assert.Empty(t, firstContainerIP(nil))
	})

	t.Run("no networks", func(t *testing.T) {
		assert.Empty(t, firstContainerIP(&container.NetworkSettings{}))
	})

	t.Run("skips nil and invalid endpoints", func(t *testing.T) {
		ns := &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"empty": nil,
			"blank": {}, // zero netip.Addr is invalid
		}}
		assert.Empty(t, firstContainerIP(ns))
	})

	t.Run("returns a valid bridge IP", func(t *testing.T) {
		ns := &container.NetworkSettings{Networks: map[string]*network.EndpointSettings{
			"bridge": {IPAddress: netip.MustParseAddr("172.17.0.4")},
		}}
		assert.Equal(t, "172.17.0.4", firstContainerIP(ns))
	})
}
