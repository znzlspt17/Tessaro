package app

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

func WriteTrace(path string, events []any) error {
	f, err := os.OpenFile(path+".trace.jsonl", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create trace: %w", err)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, event := range events {
		if err = enc.Encode(event); err != nil {
			f.Close()
			return fmt.Errorf("write trace: %w", err)
		}
	}
	if err = w.Flush(); err != nil {
		f.Close()
		return fmt.Errorf("flush trace: %w", err)
	}
	return f.Close()
}
