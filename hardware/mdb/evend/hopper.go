package evend

import (
	"context"
	"fmt"
	"time"

	"github.com/temoto/vender/engine"
	"github.com/temoto/vender/engine/inventory"
	"github.com/temoto/vender/head/state"
)

type DeviceHopper struct {
	Generic

	runTimeout time.Duration
	stock      *inventory.Stock
}

func (self *DeviceHopper) Init(ctx context.Context, addr uint8, nameSuffix string) error {
	config := state.GetConfig(ctx)
	// TODO read config
	self.runTimeout = 200 * time.Millisecond
	name := "hopper" + nameSuffix
	err := self.Generic.Init(ctx, addr, name, proto2)

	self.stock = config.Global().Inventory.Register(name)

	e := engine.ContextValueEngine(ctx, engine.ContextKey)
	e.Register(fmt.Sprintf("mdb.evend.%s_run(1)", name), self.NewRun().(engine.ArgApplier).Apply(1))
	e.Register(fmt.Sprintf("mdb.evend.%s_run(2)", name), self.NewRun().(engine.ArgApplier).Apply(2))

	return err
}

func (self *DeviceHopper) NewRun() engine.Doer {
	tag := fmt.Sprintf("mdb.evend.%s.run", self.dev.Name)

	do := engine.FuncArg{Name: tag, F: func(ctx context.Context, arg engine.Arg) error {
		units := uint8(arg)

		if err := self.Generic.NewWaitReady(tag).Do(ctx); err != nil {
			return err
		}

		if err := self.Generic.txAction([]byte{units}); err != nil {
			return err
		}

		return self.Generic.NewWaitDone(tag, self.runTimeout*time.Duration(units)).Do(ctx)
	}}

	return self.stock.WrapArg(do)
}
