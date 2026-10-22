package main

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
)

//go:embed page.html
var page []byte

// dataMarker is replaced by the model JSON. encoding/json escapes <, > and &,
// so the JSON cannot end the <script> element it sits in.
var dataMarker = []byte("__ARCHMAP_DATA__")

func render(w io.Writer, m *Model) error {
	if bytes.Count(page, dataMarker) != 1 {
		return fmt.Errorf("page.html must contain %s exactly once", dataMarker)
	}
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = w.Write(bytes.Replace(page, dataMarker, data, 1))
	return err
}
