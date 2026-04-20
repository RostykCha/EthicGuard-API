package store

import (
	"encoding/json"
	"fmt"
)

// unmarshalJSONBytes is a tiny helper used by multi-row scans where a JSONB
// column may be empty (nil bytes) and we want to leave the destination at
// its zero value in that case.
func unmarshalJSONBytes(raw []byte, dst any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("unmarshal jsonb: %w", err)
	}
	return nil
}
