package app

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// 2026-11-05: 여러 실행이 같은 날짜에 기록해도 기존 결과를 덮어쓰지 않도록 배타적으로 생성한다.
func WriteResult(w io.Writer, dir string, now time.Time, value any) error {
	_, err := WriteResultWithPath(w, dir, now, value)
	return err
}
func WriteResultWithPath(w io.Writer, dir string, now time.Time, value any) (string, error) {
	var data bytes.Buffer
	if err := EncodeJSON(&data, value); err != nil {
		return "", fmt.Errorf("encode result: %w", err)
	}
	n, err := w.Write(data.Bytes())
	if err != nil {
		return "", fmt.Errorf("write stdout: %w", err)
	}
	if n != data.Len() {
		return "", fmt.Errorf("write stdout: %w", io.ErrShortWrite)
	}
	if err = os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create output directory: %w", err)
	}
	kst := now.In(time.FixedZone("KST", 9*60*60))
	day := fmt.Sprintf("%04d-%02d-%02d", kst.Year(), kst.Day(), kst.Month())
	for i := 1; i <= 999; i++ {
		path := filepath.Join(dir, fmt.Sprintf("%s.%03d", day, i))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create output file: %w", err)
		}
		written, writeErr := f.Write(data.Bytes())
		if writeErr == nil && written != data.Len() {
			writeErr = io.ErrShortWrite
		}
		closeErr := f.Close()
		if writeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("write output file: %w", writeErr)
		}
		if closeErr != nil {
			_ = os.Remove(path)
			return "", fmt.Errorf("close output file: %w", closeErr)
		}
		return path, nil
	}
	return "", fmt.Errorf("output sequence exhausted for %s", day)
}
