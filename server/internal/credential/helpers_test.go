package credential

import (
	"encoding/json"
	"fmt"
)

func fmtSprintf(f string, a ...any) string { return fmt.Sprintf(f, a...) }
func jsonMarshal(v any) ([]byte, error)    { return json.Marshal(v) }
