package levelcheck

import (
	"context"

	"github.com/wyw14/cry-96/internal/journal"
	"github.com/wyw14/cry-96/internal/model"
)

// Recovery rebuilds the verification registry from the journal so a restart
// keeps historical calibration versions and trusted readings intact.
type Recovery struct {
	replayer *journal.Replayer
	service  *Service
}

func NewRecovery(replayer *journal.Replayer, service *Service) *Recovery {
	return &Recovery{replayer: replayer, service: service}
}

func (r *Recovery) RestoreAll(ctx context.Context) error {
	states, err := r.replayer.ReplayAll(ctx)
	if err != nil {
		return err
	}
	for _, state := range states {
		for _, event := range state.Applied {
			switch event.Type {
			case model.EventLevelPointRegistered, model.EventLevelCalibrationIssued,
				model.EventLevelManualRecorded, model.EventLevelReadingVerified:
				if err := r.service.Restore(event); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
