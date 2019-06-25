package mdb

import (
	"testing"

	"github.com/stretchr/testify/require"
)

var PH = MustPacketFromHex

// Yo Dawg I heard you like testing
func TestTestMdberExpectSync(t *testing.T) {
	t.Parallel()

	m, mock := NewTestMdber(t)
	defer mock.Close()
	wait := make(chan struct{})
	go func() {
		require.Nil(t, m.Tx(PH("30", true), new(Packet)))
		// require.Nil(t, m.Tx(PH("0b", true), new(Packet)))
		wait <- struct{}{}
	}()
	mock.Expect([]MockR{
		{"30", ""},
		// {"31", ""},
	})
	<-wait
}
func TestTestMdberExpectBg(t *testing.T) {
	t.Parallel()

	m, mock := NewTestMdber(t)
	defer mock.Close()
	go mock.Expect([]MockR{
		{"30", ""},
		// {"31", ""},
	})
	require.Nil(t, m.Tx(PH("30", true), new(Packet)))
	// require.Nil(t, m.Tx(PH("0b", true), new(Packet)))
}
