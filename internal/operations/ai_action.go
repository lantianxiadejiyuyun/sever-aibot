package operations

import (
	"context"
	"time"

	"server-aibot/internal/store"
)

// RunApproved revalidates an AI approval while the check definition is leased.
// The callback must only validate; it must not execute or edit the check.
func (s *Service) RunApproved(ctx context.Context, id string, validate func(context.Context, store.OperationCheck) error) (store.InspectionRun, error) {
	select {
	case s.slots <- struct{}{}:
	default:
		return store.InspectionRun{}, ErrBusy
	}
	defer func() { <-s.slots }()
	claimed, err := s.Store.ClaimOperationCheck(ctx, id, true)
	if err != nil {
		return store.InspectionRun{}, err
	}
	if !claimed {
		return store.InspectionRun{}, ErrBusy
	}
	// Also release on a stale approval, without manufacturing an inspection run.
	executing := false
	defer func() {
		if executing {
			return
		}
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := s.Store.DB().ExecContext(cleanup, `UPDATE operation_checks SET lease_until=0 WHERE id=?`, id)
		if err != nil {
			s.logError("release approved inspection lease", err)
		}
	}()
	c, err := s.Store.GetOperationCheck(ctx, id)
	if err != nil {
		return store.InspectionRun{}, err
	}
	if err = validate(ctx, c); err != nil {
		return store.InspectionRun{}, err
	}
	executing = true // execute owns lease release after it persists the result.
	return s.execute(ctx, c)
}
