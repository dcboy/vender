// Sorry, workaround to import cycles.
package state_new

import (
	"context"
	"testing"

	"github.com/juju/errors"
	"github.com/temoto/alive"
	"github.com/temoto/vender/engine"
	"github.com/temoto/vender/engine/inventory"
	"github.com/temoto/vender/hardware/mdb"
	tele_api "github.com/temoto/vender/head/tele/api"
	"github.com/temoto/vender/log2"
	"github.com/temoto/vender/state"
)

func NewContext(log *log2.Log, teler tele_api.Teler) (context.Context, *state.Global) {
	if log == nil {
		panic("code error NewContext() log=nil")
	}

	g := &state.Global{
		Alive:     alive.NewAlive(),
		Engine:    engine.NewEngine(log),
		Inventory: new(inventory.Inventory),
		Log:       log,
		Tele:      teler,
	}
	ctx := context.Background()
	ctx = context.WithValue(ctx, log2.ContextKey, log)
	ctx = context.WithValue(ctx, state.ContextKey, g)

	return ctx, g
}

func NewTestContext(t testing.TB, confString string) (context.Context, *state.Global) {
	fs := state.NewMockFullReader(map[string]string{
		"test-inline": confString,
	})

	log := log2.NewTest(t, log2.LDebug)
	// log := log2.NewStderr(log2.LDebug) // useful with panics
	log.SetFlags(log2.LTestFlags)
	ctx, g := NewContext(log, tele_api.NewStub())
	g.MustInit(ctx, state.MustReadConfig(log, fs, "test-inline"))

	mdbus, mdbMock := mdb.NewMockBus(t)
	g.Hardware.Mdb.Bus = mdbus
	if _, err := g.Mdb(); err != nil {
		t.Fatal(errors.Trace(err))
	}
	ctx = context.WithValue(ctx, mdb.MockContextKey, mdbMock)

	return ctx, g
}