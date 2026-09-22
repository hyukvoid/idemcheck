package report

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/hyukvoid/idemcheck/internal/models"
)

// JSON writes the stable machine-readable result model.
func JSON(w io.Writer, res *models.Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(res); err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	return nil
}
