package ui

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/temoto/alive"
	"github.com/temoto/errors"
	"github.com/temoto/vender/hardware/input"
	"github.com/temoto/vender/hardware/lcd"
	"github.com/temoto/vender/hardware/mdb/evend"
	"github.com/temoto/vender/head/money"
	"github.com/temoto/vender/helpers"
	"github.com/temoto/vender/state"
)

const (
	DefaultCream = 4
	MaxCream     = 6
	DefaultSugar = 4
	MaxSugar     = 8
)

// TODO extract text messages to catalog
const (
	msgCream  = "Сливки"
	msgSugar  = "Сахар"
	msgCredit = "Кредит:"

	msgMenuCodeEmpty          = "нажимайте цифры"
	msgMenuCodeInvalid        = "проверьте код"
	msgMenuInsufficientCredit = "добавьте денег"

	msgInputCode = "код:%s\x00"
)

const (
	frontModeStatus = "menu-status"
	frontModeCream  = "cream"
	frontModeSugar  = "sugar"
	frontModeBroken = "broken"
)

const modCreamSugarTimeout = 3 * time.Second

var ScaleAlpha = []byte{
	0x94, // empty
	0x95,
	0x96,
	0x97, // full
	// '0', '1', '2', '3',
}

type UIFront struct {
	// config
	Finish       func(context.Context, *UIMenuResult)
	resetTimeout time.Duration

	// state
	g       *state.Global
	broken  bool
	menu    Menu
	display *lcd.TextDisplay // FIXME
	result  UIMenuResult
}

type UIMenuResult struct {
	Item    MenuItem
	Confirm bool
	Cream   uint8
	Sugar   uint8
}

func NewUIFront(ctx context.Context, menu Menu) *UIFront {
	self := &UIFront{
		g:    state.GetGlobal(ctx),
		menu: menu,
		result: UIMenuResult{
			// TODO read config
			Cream: DefaultCream,
			Sugar: DefaultSugar,
		},
	}
	self.display = self.g.Hardware.HD44780.Display
	self.resetTimeout = helpers.IntSecondDefault(self.g.Config.UI.Front.ResetTimeoutSec, 0)

	return self
}

func (self *UIFront) SetBroken(flag bool) {
	self.g.Log.Infof("uifront mode = broken")
	self.broken = flag
	self.g.Tele.Broken(flag)
}

func (self *UIFront) Tag() string { return "ui-front" }

func (self *UIFront) Run(ctx context.Context, alive *alive.Alive) {
	alive.Add(1)
	inputTag := self.Tag()
	defer func() {
		// stop pending AcceptCredit and SubscribeChan
		alive.Stop()
		self.g.Hardware.Input.Unsubscribe(inputTag)
		self.Finish(ctx, &self.result)

		alive.Done() // let caller UILoop observe me finished
	}()

	config := self.g.Config.UI.Front
	moneysys := money.GetGlobal(ctx)
	timer := time.NewTicker(200 * time.Millisecond)
	inputBuf := make([]byte, 0, 32)
	maxPrice := self.menu.MaxPrice() // TODO decide if this should be recalculated during ui
	moneych := make(chan money.Event)
	stopch := alive.StopChan()
	inputCh := self.g.Hardware.Input.SubscribeChan(inputTag, stopch)

init:
	if self.broken {
		moneysys.AcceptCredit(ctx, 0, nil, nil)
	} else {
		go moneysys.AcceptCredit(ctx, maxPrice, stopch, moneych)
	}
	self.result = UIMenuResult{
		Cream: DefaultCream,
		Sugar: DefaultSugar,
	}
	inputBuf = inputBuf[:0]
	mode := frontModeStatus
	lastActivity := time.Now()

	for alive.IsRunning() {
		// step 1: refresh display
		if self.broken {
			mode = frontModeBroken
		}
		credit := moneysys.Credit(ctx)
		switch mode {
		case frontModeStatus:
			l1 := config.MsgStateIntro
			l2 := ""
			if (credit != 0) || (len(inputBuf) > 0) {
				l1 = msgCredit + credit.Format100I()
				l2 = fmt.Sprintf(msgInputCode, string(inputBuf))
			} else {
				if doCheckTempHot := self.g.Engine.Resolve("mdb.evend.valve_check_temp_hot"); doCheckTempHot != nil {
					err := doCheckTempHot.Validate()
					if errtemp, ok := err.(*evend.ErrWaterTemperature); ok {
						l2 = fmt.Sprintf("no hot water %d", errtemp.Current)
					}
				}
			}
			self.display.SetLines(l1, l2)
		case frontModeBroken:
			self.display.SetLines(config.MsgStateBroken, "")
		}

		// step 2: wait for input/timeout
	waitInput:
		var e input.Event
		select {
		case e = <-inputCh:
			lastActivity = time.Now()

		case em := <-moneych:
			lastActivity = time.Now()

			switch em.Name() {
			case money.EventAbort:
				self.g.Error(errors.Trace(moneysys.Abort(ctx)))
			}
			// likely redundant
			credit = moneysys.Credit(ctx)

			go moneysys.AcceptCredit(ctx, maxPrice, stopch, moneych)

		case <-timer.C:
			inactive := time.Since(lastActivity)
			switch {
			case (mode == frontModeCream || mode == frontModeSugar) && (inactive >= modCreamSugarTimeout):
				lastActivity = time.Now()
				mode = frontModeStatus // "return to previous mode"
				goto handleEnd
			case inactive >= self.resetTimeout:
				goto init
			default:
				goto waitInput
			}
		}

		// step 3: handle input/timeout
		switch mode {
		case frontModeStatus:
			switch e.Key {
			case input.EvendKeyCreamLess, input.EvendKeyCreamMore, input.EvendKeySugarLess, input.EvendKeySugarMore:
				mode = self.handleCreamSugar(mode, e)
				goto handleEnd
			}

			switch {
			case e.IsDigit():
				inputBuf = append(inputBuf, byte(e.Key))

			case input.IsReject(&e):
				// backspace semantic
				if len(inputBuf) > 0 {
					inputBuf = inputBuf[:len(inputBuf)-1]
					break
				}

				self.result = UIMenuResult{Confirm: false}
				return

			case input.IsAccept(&e):
				if len(inputBuf) == 0 {
					self.showError(inputCh, msgMenuCodeEmpty)
					break
				}

				x, err := strconv.ParseUint(string(inputBuf), 10, 16)
				if err != nil {
					inputBuf = inputBuf[:0]
					self.showError(inputCh, msgMenuCodeInvalid)
					break
				}
				code := uint16(x)

				mitem, ok := self.menu[code]
				if !ok {
					self.showError(inputCh, msgMenuCodeInvalid)
					break
				}
				self.g.Log.Debugf("compare price=%v credit=%v", mitem.Price, credit)
				if mitem.Price > credit {
					self.showError(inputCh, msgMenuInsufficientCredit)
					break
				}

				self.result.Confirm = true
				self.result.Item = mitem
				return
			}

		case frontModeCream, frontModeSugar:
			switch e.Key {
			case input.EvendKeyCreamLess, input.EvendKeyCreamMore, input.EvendKeySugarLess, input.EvendKeySugarMore:
				mode = self.handleCreamSugar(mode, e)
				goto handleEnd
			}
			if input.IsAccept(&e) || input.IsReject(&e) {
				mode = frontModeStatus // "return to previous mode"
			}
		}
	handleEnd:
	}

	// external stop
	self.result = UIMenuResult{Confirm: false}
}

func (self *UIFront) showError(inputch chan input.Event, text string) {
	const timeout = 10 * time.Second

	self.display.Message(self.g.Config.UI.Front.MsgError, text, func() {
		select {
		case <-inputch:
		case <-time.After(timeout):
		}
	})
}

func (self *UIFront) handleCreamSugar(mode string, e input.Event) string {
	switch e.Key {
	case input.EvendKeyCreamLess:
		if self.result.Cream > 0 {
			self.result.Cream--
			//lint:ignore SA9003 empty branch
		} else {
			// TODO notify "impossible input" (sound?)
		}
	case input.EvendKeyCreamMore:
		if self.result.Cream < MaxCream {
			self.result.Cream++
			//lint:ignore SA9003 empty branch
		} else {
			// TODO notify "impossible input" (sound?)
		}
	case input.EvendKeySugarLess:
		if self.result.Sugar > 0 {
			self.result.Sugar--
			//lint:ignore SA9003 empty branch
		} else {
			// TODO notify "impossible input" (sound?)
		}
	case input.EvendKeySugarMore:
		if self.result.Sugar < MaxSugar {
			self.result.Sugar++
			//lint:ignore SA9003 empty branch
		} else {
			// TODO notify "impossible input" (sound?)
		}
	default:
		return mode
	}
	var t1, t2 []byte
	switch e.Key {
	case input.EvendKeyCreamLess, input.EvendKeyCreamMore:
		t1 = self.display.Translate(fmt.Sprintf("%s  /%d", msgCream, self.result.Cream))
		t2 = formatScale(self.result.Cream, 0, MaxCream, ScaleAlpha)
		mode = frontModeCream
	case input.EvendKeySugarLess, input.EvendKeySugarMore:
		t1 = self.display.Translate(fmt.Sprintf("%s  /%d", msgSugar, self.result.Sugar))
		t2 = formatScale(self.result.Sugar, 0, MaxSugar, ScaleAlpha)
		mode = frontModeSugar
	}
	t2 = append(append(append(make([]byte, 0, lcd.MaxWidth), '-', ' '), t2...), ' ', '+')
	self.display.SetLinesBytes(
		self.display.JustCenter(t1),
		self.display.JustCenter(t2),
	)
	return mode
}

// tightly coupled to len(alphabet)=4
func formatScale(value, min, max uint8, alphabet []byte) []byte {
	var vicon [6]byte
	switch value {
	case min:
		vicon[0], vicon[1], vicon[2], vicon[3], vicon[4], vicon[5] = 0, 0, 0, 0, 0, 0
	case max:
		vicon[0], vicon[1], vicon[2], vicon[3], vicon[4], vicon[5] = 3, 3, 3, 3, 3, 3
	default:
		rng := uint16(max) - uint16(min)
		part := uint8((float32(value-min) / float32(rng)) * 24)
		// log.Printf("scale(%d,%d..%d) part=%d", value, min, max, part)
		for i := 0; i < len(vicon); i++ {
			if part >= 4 {
				vicon[i] = 3
				part -= 4
			} else {
				vicon[i] = part
				break
			}
		}
	}
	for i := 0; i < len(vicon); i++ {
		vicon[i] = alphabet[vicon[i]]
	}
	return vicon[:]
}
