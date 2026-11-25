package operations

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"server-aibot/internal/store"
)

func (s *Service) StartBatch(ctx context.Context, name string, ids []string) (store.InspectionBatch, error) {
	if strings.TrimSpace(name) == "" || len(name) > 120 {
		return store.InspectionBatch{}, errors.New("批次名称不能为空且最多 120 字节")
	}
	if len(ids) < 1 || len(ids) > 50 {
		return store.InspectionBatch{}, errors.New("每批次必须选择 1 到 50 个检查")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] {
			return store.InspectionBatch{}, errors.New("批次检查不能重复")
		}
		seen[id] = true
		if _, err := s.Store.GetOperationCheck(ctx, id); err != nil {
			return store.InspectionBatch{}, err
		}
	}
	s.lifecycle.Lock()
	defer s.lifecycle.Unlock()
	if s.closed {
		return store.InspectionBatch{}, errors.New("巡检服务已关闭")
	}
	if len(s.batchCancels) >= 1 {
		return store.InspectionBatch{}, ErrBusy
	}
	batch, err := s.Store.CreateInspectionBatch(ctx, name, ids)
	if err != nil {
		return batch, err
	}
	runCtx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	s.batchCancels[batch.ID] = cancel
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer cancel()
		defer func() { s.lifecycle.Lock(); delete(s.batchCancels, batch.ID); s.lifecycle.Unlock() }()
		s.runBatch(runCtx, batch)
	}()
	return batch, nil
}
func (s *Service) runBatch(ctx context.Context, b store.InspectionBatch) {
	type outcome struct {
		run     store.InspectionRun
		failure store.InspectionBatchFailure
	}
	jobs := make(chan string, len(b.CheckIDs))
	results := make(chan outcome, len(b.CheckIDs))
	for _, id := range b.CheckIDs {
		jobs <- id
	}
	close(jobs)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for id := range jobs {
				var run store.InspectionRun
				var err error
				for {
					if ctx.Err() != nil {
						err = ctx.Err()
						break
					}
					run, err = s.Run(ctx, id)
					if !errors.Is(err, ErrBusy) {
						break
					}
					timer := time.NewTimer(200 * time.Millisecond)
					select {
					case <-ctx.Done():
						timer.Stop()
					case <-timer.C:
					}
				}
				if err != nil {
					results <- outcome{failure: store.InspectionBatchFailure{CheckID: id, Message: "检查未完成：目标已删除、服务停止或执行超时"}}
				} else {
					results <- outcome{run: run}
				}
			}
		}()
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result.run.ID != "" {
			b.Runs = append(b.Runs, result.run)
		} else {
			b.Failures = append(b.Failures, result.failure)
		}
	}
	b.State = "complete"
	if ctx.Err() != nil {
		b.State = "interrupted"
	}
	saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Store.FinishInspectionBatch(saveCtx, b); err != nil {
		s.logError("save inspection batch", err)
	}
}
