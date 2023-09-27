package bgpipe

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/knadh/koanf/providers/posflag"
	"github.com/rs/zerolog"
)

// Usage prints CLI usage screen
func (b *Bgpipe) Usage() {
	fmt.Fprintf(os.Stderr, `Usage: bgpipe [OPTIONS] [--] STAGE [STAGE-OPTIONS] [STAGE-ARGUMENTS...] [--] ...

Options:
`)
	b.F.PrintDefaults()
	fmt.Fprintf(os.Stderr, `
Supported stages (run stage -h to get its help)
`)

	// iterate over cmds
	var cmds []string
	for cmd := range b.repo {
		cmds = append(cmds, cmd)
	}
	sort.Strings(cmds)
	for _, cmd := range cmds {
		var descr string

		s := b.NewStage(cmd)
		if s != nil {
			descr = s.Descr
		}

		fmt.Fprintf(os.Stderr, "  %-22s %s\n", cmd, descr)
	}
	fmt.Fprintf(os.Stderr, "\n")
}

// configure configures bgpipe
func (b *Bgpipe) configure() error {
	// parse CLI args
	err := b.parseArgs(os.Args[1:])
	if err != nil {
		return fmt.Errorf("could not parse CLI flags: %w", err)
	}

	// debugging level
	if ll := b.K.String("log"); len(ll) > 0 {
		lvl, err := zerolog.ParseLevel(ll)
		if err != nil {
			return err
		}
		zerolog.SetGlobalLevel(lvl)
	}

	return nil
}

// parseArgs adds and configures stages from CLI args
func (b *Bgpipe) parseArgs(args []string) error {
	// parse and export flags into koanf
	if err := b.F.Parse(args); err != nil {
		return err
	} else {
		b.K.Load(posflag.Provider(b.F, ".", b.K), nil)
	}

	// parse stages and their args
	args = b.F.Args()
	for idx := 0; len(args) > 0; idx++ {
		// skip empty stages
		if args[0] == "--" {
			args = args[1:]
			continue
		}

		// is args[0] a special value, or generic stage command name?
		cmd := args[0]
		switch {
		case IsAddr(cmd):
			cmd = "tcp"
		case IsFile(cmd):
			cmd = "mrt" // TODO: stat -> mrt / exec / json / etc.
		default:
			args = args[1:]
		}

		// get s for cmd
		s, err := b.AddStage(idx, cmd)
		if err != nil {
			return err
		}

		// find an explicit end of its args
		var nextargs []string
		for i, arg := range args {
			if arg == "--" {
				nextargs = args[i+1:]
				args = args[:i]
				break
			}
		}

		// parse stage args, move on
		if remargs, err := s.parseArgs(args); err != nil {
			return err
		} else {
			args = append(remargs, nextargs...)
		}
	}

	return nil
}

// parseArgs parses CLI flags and arguments, exporting to K.
// May return unused args.
func (s *StageBase) parseArgs(args []string) (unused []string, err error) {
	// override s.Flags.Usage?
	if s.Flags.Usage == nil {
		if len(s.Usage) == 0 {
			s.Usage = strings.ToUpper(strings.Join(s.Args, " "))
		}
		s.Flags.Usage = func() {
			fmt.Fprintf(os.Stderr, "Stage usage: %s %s\n", s.Cmd, s.Usage)
			fmt.Fprint(os.Stderr, s.Flags.FlagUsages())
		}
	}

	// parse stage flags, export to koanf
	if err := s.Flags.Parse(args); err != nil {
		return args, s.Errorf("%w", err)
	} else {
		s.K.Load(posflag.Provider(s.Flags, ".", s.K), nil)
	}

	// uses CLI arguments?
	sargs := s.Flags.Args()
	if s.Args != nil {
		// special case: all arguments
		if len(s.Args) == 0 {
			s.K.Set("args", sargs)
			return nil, nil
		}

		// rewrite into k
		for _, name := range s.Args {
			if len(sargs) == 0 || sargs[0] == "--" {
				return sargs, s.Errorf("needs an argument: %s", name)
			}
			s.K.Set(name, sargs[0])
			sargs = sargs[1:]
		}
	}

	// drop explicit --
	if len(sargs) > 0 && sargs[0] == "--" {
		sargs = sargs[1:]
	}

	return sargs, nil
}

// cfgEvents returns events from given koanf key,
// or nil if none found
func (s *StageBase) cfgEvents(key string) []string {
	events := s.K.Strings(key)
	if len(events) == 0 {
		return nil
	}

	// rewrite
	for i, et := range events {
		has_pkg := strings.IndexByte(et, '.') > 0
		has_lib := strings.IndexByte(et, '/') > 0

		if has_pkg && has_lib {
			// fully specified, done
		} else if !has_lib {
			if !has_pkg {
				et = "bgpfix/pipe." + strings.ToUpper(et)
			} else {
				et = "bgpfix/" + et
			}
		} else {
			// has lib, take as-is
		}

		events[i] = et
	}

	return events
}
