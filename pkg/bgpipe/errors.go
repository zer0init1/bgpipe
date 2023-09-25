package bgpipe

import "errors"

var (
	ErrStageCmd     = errors.New("invalid stage command")
	ErrStageDiff    = errors.New("already defined but different")
	ErrStageStopped = errors.New("stage stopped")
	ErrFirstOrLast  = errors.New("must be either the first or the last stage")
)
