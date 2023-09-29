package bgpipe

import (
	"context"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/bgpfix/bgpfix/pipe"
	"github.com/knadh/koanf/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/pflag"
)

// Bgpipe represents a BGP pipeline consisting of several stages, built on top of bgpfix.Pipe
type Bgpipe struct {
	zerolog.Logger

	Ctx    context.Context
	Cancel context.CancelCauseFunc

	F      *pflag.FlagSet // global flags
	K      *koanf.Koanf   // global config
	Pipe   *pipe.Pipe     // bgpfix pipe
	Stages []*StageBase   // pipe stages

	repo map[string]NewStage // maps cmd to new stage func

	wg_lwrite sync.WaitGroup // stages that write to pipe L
	wg_lread  sync.WaitGroup // stages that read from pipe L
	wg_rwrite sync.WaitGroup // stages that write to pipe R
	wg_rread  sync.WaitGroup // stages that read from pipe R

	auto_stdin  *StageBase // if not nil, automatic stdin stage
	auto_stdout *StageBase // if not nil, automatic stdout stage
	logbuf      []byte     // buffer for LogEvent
}

// NewBgpipe creates a new bgpipe instance using given
// repositories of stage commands
func NewBgpipe(repo ...map[string]NewStage) *Bgpipe {
	b := new(Bgpipe)
	b.Ctx, b.Cancel = context.WithCancelCause(context.Background())

	// default logger
	b.Logger = log.Output(zerolog.ConsoleWriter{
		Out:        os.Stderr,
		TimeFormat: time.DateTime,
	})

	// pipe
	b.Pipe = pipe.NewPipe(b.Ctx)
	po := &b.Pipe.Options
	po.Logger = &b.Logger
	po.Lreverse = true // it's just the case for bgpipe

	// global config
	b.K = koanf.New(".")

	// global CLI flags
	b.F = pflag.NewFlagSet("bgpipe", pflag.ExitOnError)
	b.addFlags()

	// command repository
	b.repo = make(map[string]NewStage)
	for i := range repo {
		b.AddRepo(repo[i])
	}

	return b
}

// Run configures and runs the bgpipe
func (b *Bgpipe) Run() error {
	// configure bgpipe and its stages
	if err := b.Configure(); err != nil {
		b.Fatal().Err(err).Msg("configuration error")
		return err
	}

	// attach stages to pipe
	if err := b.Attach(); err != nil {
		b.Fatal().Err(err).Msg("could not attach stages to the pipe")
		return err
	}

	// attach our b.Start
	b.Pipe.Options.OnStart(b.Start)

	// start the pipeline and block
	b.Pipe.Start() // will call b.Start
	b.Pipe.Wait()  // until error or all processing is done

	// any errors on the global context?
	if err := context.Cause(b.Ctx); err != nil {
		b.Fatal().Err(err).Msg("fatal error")
		return err
	}

	// successfully finished
	return nil
}

// Start is called after the bgpfix pipe starts
func (b *Bgpipe) Start(ev *pipe.Event) bool {
	// start auto stdout?
	if s := b.auto_stdout; s != nil {
		s.WgAdd(1)
		go s.run(ev.Type)
	}

	// start auto stdin?
	if s := b.auto_stdin; s != nil {
		s.WgAdd(1)
		go s.run(ev.Type)
	}

	// go through all stages
	for _, s := range b.Stages {
		if s == nil {
			continue
		}

		// kick waitgroups now, even if waiting for --on
		s.WgAdd(1)

		// run now iff already enabled
		if s.enabled.Load() {
			go s.run(ev.Type)
		}
	}

	// wait for writers
	go func() {
		b.wg_lwrite.Wait()
		b.Debug().Msg("closing L input")
		b.Pipe.L.CloseInput()
	}()
	go func() {
		b.wg_rwrite.Wait()
		b.Debug().Msg("closing R input")
		b.Pipe.R.CloseInput()
	}()

	// wait for readers
	go func() {
		b.wg_lread.Wait()
		b.Debug().Msg("closing L output")
		b.Pipe.L.CloseOutput()
	}()
	go func() {
		b.wg_rread.Wait()
		b.Debug().Msg("closing R output")
		b.Pipe.R.CloseOutput()
	}()

	return false
}

// LogEvent logs given event
func (b *Bgpipe) LogEvent(ev *pipe.Event) bool {
	if ev.Msg != nil {
		b.logbuf = ev.Msg.ToJSON(b.logbuf[:0])
	} else {
		b.logbuf = b.logbuf[:0]
	}

	b.
		Err(ev.Error). // will b.Info() if nil
		Uint64("seq", ev.Seq).
		Bytes("msg", b.logbuf).
		Interface("val", ev.Value).
		Msgf("event %s", ev.Type)
	return true
}

// AddRepo adds mapping between stage commands and their NewStageFunc
func (b *Bgpipe) AddRepo(cmds map[string]NewStage) {
	for cmd, newfunc := range cmds {
		b.repo[cmd] = newfunc
	}
}

// AddStage adds and returns a new stage at idx for cmd,
// or returns an existing instance if it's for the same cmd.
func (b *Bgpipe) AddStage(idx int, cmd string) (*StageBase, error) {
	// append?
	if idx <= 0 {
		idx = max(1, len(b.Stages))
	}

	// already there? check cmd
	if idx < len(b.Stages) {
		if s := b.Stages[idx]; s != nil {
			if s.Cmd == cmd {
				return s, nil
			} else {
				return nil, fmt.Errorf("[%d] %s: %w: %s", idx, cmd, ErrStageDiff, s.Cmd)
			}
		}
	}

	// create
	s := b.NewStage(cmd)
	if s == nil {
		return nil, fmt.Errorf("[%d] %s: %w", idx, cmd, ErrStageCmd)
	}

	// store
	for idx >= len(b.Stages) {
		b.Stages = append(b.Stages, nil)
	}
	b.Stages[idx] = s
	s.Index = idx

	return s, nil
}
