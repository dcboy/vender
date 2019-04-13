package evend

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/juju/errors"
	"github.com/temoto/vender/engine"
	"github.com/temoto/vender/engine/inventory"
	"github.com/temoto/vender/hardware/mdb"
	"github.com/temoto/vender/head/state"
)

const DefaultValveRate float32 = 1.538462
const DefaultValveRateRev float32 = 1 / 1.538462

const (
	valvePollBusy   = 0x10
	valvePollNotHot = 0x40
)

type DeviceValve struct {
	Generic

	pourTimeout time.Duration
	tempHot     uint8
	waterStock  *inventory.Stock
}

func (self *DeviceValve) Init(ctx context.Context) error {
	config := state.GetConfig(ctx)
	// TODO read config
	self.pourTimeout = 30 * time.Second
	self.proto2BusyMask = valvePollBusy
	self.proto2IgnoreMask = valvePollNotHot
	err := self.Generic.Init(ctx, 0xc0, "valve", proto2)

	self.waterStock = config.Global().Inventory.Register("water", DefaultValveRateRev)

	e := engine.ContextValueEngine(ctx, engine.ContextKey)
	e.Register("mdb.evend.valve_get_temp_hot", self.NewGetTempHot())
	e.Register("mdb.evend.valve_set_temp_hot(70)", self.NewSetTempHot().(engine.ArgApplier).Apply(70))
	e.Register("mdb.evend.valve_pour_coffee(120)", self.NewPourCoffee().(engine.ArgApplier).Apply(120))
	e.Register("mdb.evend.valve_pour_cold(120)", self.NewPourCold().(engine.ArgApplier).Apply(120))
	e.Register("mdb.evend.valve_pour_hot(120)", self.NewPourHot().(engine.ArgApplier).Apply(120))
	e.Register("mdb.evend.valve_cold_open", self.NewValveCold(true))
	e.Register("mdb.evend.valve_cold_close", self.NewValveCold(false))
	e.Register("mdb.evend.valve_hot_open", self.NewValveHot(true))
	e.Register("mdb.evend.valve_hot_close", self.NewValveHot(false))
	e.Register("mdb.evend.valve_boiler_open", self.NewValveBoiler(true))
	e.Register("mdb.evend.valve_boiler_close", self.NewValveBoiler(false))
	e.Register("mdb.evend.valve_pump_coffee_start", self.NewPumpCoffee(true))
	e.Register("mdb.evend.valve_pump_coffee_stop", self.NewPumpCoffee(false))
	e.Register("mdb.evend.valve_pump_start", self.NewPump(true))
	e.Register("mdb.evend.valve_pump_stop", self.NewPump(false))

	return err
}

func (self *DeviceValve) MlToTimeout(ml uint16) time.Duration {
	const min = 500 * time.Millisecond
	const perMl = 50 * time.Millisecond // FIXME
	return min + time.Duration(ml)*perMl
}

func (self *DeviceValve) NewGetTempHot() engine.Doer {
	const tag = "mdb.evend.valve.get_temp_hot"

	return engine.Func{Name: tag, F: func(ctx context.Context) error {
		bs := []byte{self.dev.Address + 4, 0x11}
		request := mdb.MustPacketFromBytes(bs, true)
		r := self.dev.Tx(request)
		if r.E != nil {
			return errors.Annotate(r.E, tag)
		}
		bs = r.P.Bytes()
		self.dev.Log.Debugf("%s request=%s response=(%d)%s", tag, request.Format(), r.P.Len(), r.P.Format())
		if len(bs) != 1 {
			return errors.NotValidf("%s response=%x", tag, bs)
		}
		self.tempHot = bs[0]
		return nil
	}}
}

func (self *DeviceValve) NewSetTempHot() engine.Doer {
	const tag = "mdb.evend.valve.set_temp_hot"

	return engine.FuncArg{Name: tag, F: func(ctx context.Context, arg engine.Arg) error {
		temp := uint8(arg)
		bs := []byte{self.dev.Address + 5, 0x10, temp}
		request := mdb.MustPacketFromBytes(bs, true)
		r := self.dev.Tx(request)
		if r.E != nil {
			return errors.Annotate(r.E, tag)
		}
		self.dev.Log.Debugf("%s request=%s response=(%d)%s", tag, request.Format(), r.P.Len(), r.P.Format())
		return nil
	}}
}

func (self *DeviceValve) newPourCareful(name string, arg1 byte, abort engine.Doer) engine.Doer {
	tagPour := "pour_" + name
	tag := "mdb.evend.valve.%s" + tagPour
	const cautionPartMl = 20

	doPour := engine.FuncArg{
		Name: tag + "/careful",
		F: func(ctx context.Context, arg engine.Arg) error {
			ml := uint16(arg)
			if ml > cautionPartMl {
				cautionTimeout := self.MlToTimeout(cautionPartMl)
				err := self.newCommand(tagPour, strconv.Itoa(int(cautionPartMl)), arg1, self.MlToUnit(cautionPartMl)).Do(ctx)
				if err != nil {
					return err
				}
				err = self.NewWaitDone(tag, cautionTimeout).Do(ctx)
				if err != nil {
					abort.Do(ctx)
					return err
				}
				ml -= cautionPartMl
			}
			err := self.newCommand(tagPour, strconv.Itoa(int(ml)), arg1, self.MlToUnit(ml)).Do(ctx)
			if err != nil {
				return err
			}
			err = self.NewWaitDone(tag, self.MlToTimeout(ml)).Do(ctx)
			return err
		}}

	tx := engine.NewTree(tag)
	tx.Root.
		Append(self.NewWaitReady(tag)).
		Append(doPour)
	return tx
}

func (self *DeviceValve) NewPourHot() engine.Doer {
	tag := fmt.Sprintf("%s.pour_hot", self.dev.Name)
	tx := engine.NewTree(tag)
	tx.Root.
		Append(self.NewWaitReady(tag)).
		Append(self.newPour(tag, 0x01)).
		Append(self.NewWaitDone(tag, self.pourTimeout))
	return self.waterStock.WrapArg(tx)
}

func (self *DeviceValve) NewPourCold() engine.Doer {
	tag := fmt.Sprintf("%s.pour_cold", self.dev.Name)
	tx := engine.NewTree(tag)
	tx.Root.
		Append(self.NewWaitReady(tag)).
		Append(self.newPour(tag, 0x02)).
		Append(self.NewWaitDone(tag, self.pourTimeout))
	return self.waterStock.WrapArg(tx)
}

func (self *DeviceValve) NewPourCoffee() engine.Doer {
	tx := self.newPourCareful("coffee", 0x03, self.NewPumpCoffee(false))
	return self.waterStock.WrapArg(tx)
}

func (self *DeviceValve) NewValveCold(open bool) engine.Doer {
	if open {
		return self.newCommand("valve_cold", "open", 0x10, 0x01)
	} else {
		return self.newCommand("valve_cold", "close", 0x10, 0x00)
	}
}
func (self *DeviceValve) NewValveHot(open bool) engine.Doer {
	if open {
		return self.newCommand("valve_hot", "open", 0x11, 0x01)
	} else {
		return self.newCommand("valve_hot", "close", 0x11, 0x00)
	}
}
func (self *DeviceValve) NewValveBoiler(open bool) engine.Doer {
	if open {
		return self.newCommand("valve_boiler", "open", 0x12, 0x01)
	} else {
		return self.newCommand("valve_boiler", "close", 0x12, 0x00)
	}
}
func (self *DeviceValve) NewPumpCoffee(start bool) engine.Doer {
	if start {
		return self.newCommand("pump_coffee", "start", 0x13, 0x01)
	} else {
		return self.newCommand("pump_coffee", "stop", 0x13, 0x00)
	}
}
func (self *DeviceValve) NewPump(start bool) engine.Doer {
	if start {
		return self.newCommand("pump", "start", 0x14, 0x01)
	} else {
		return self.newCommand("pump", "stop", 0x14, 0x00)
	}
}

func (self *DeviceValve) newPour(tag string, b1 byte) engine.Doer {
	return engine.FuncArg{
		Name: tag,
		F: func(ctx context.Context, arg engine.Arg) error {
			bs := []byte{b1, uint8(self.waterStock.TranslateArg(arg))}
			return self.txAction(bs)
		},
	}
}

func (self *DeviceValve) newCommand(cmdName, argName string, arg1, arg2 byte) engine.Doer {
	tag := fmt.Sprintf("mdb.evend.valve.%s:%s", cmdName, argName)
	return self.Generic.NewAction(tag, arg1, arg2)
}
