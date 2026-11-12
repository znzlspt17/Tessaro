//go:build windows

package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"jevcancode/internal/application"
)

// 2026-11-08: 큰 trace도 원본을 배치 단위로 읽어 내보내기 메모리를 제한한다.
func exportTraceFile(ctx context.Context, manager *application.Manager, id, path string) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".trace-export-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	w := bufio.NewWriterSize(f, 64<<10)
	encoder := json.NewEncoder(w)
	var cursor application.TraceCursor
	for {
		select {
		case <-ctx.Done():
			_ = f.Close()
			return ctx.Err()
		default:
		}
		events, next, more, e := manager.ReadTraceBatch(id, cursor, 512)
		if e != nil {
			_ = f.Close()
			return e
		}
		for _, event := range events {
			if e = encoder.Encode(event); e != nil {
				_ = f.Close()
				return e
			}
		}
		cursor = next
		if !more {
			break
		}
	}
	if err = w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
