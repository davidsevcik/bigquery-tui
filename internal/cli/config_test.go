package cli

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/davidsevcik/bigquery-tui/pkg/api"
)

const waitHint = "VISUAL='code --wait'"

func oneEnvelope(t *testing.T, out string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var doc json.RawMessage
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout holds no JSON document: %v\n%s", err, out)
	}
	if err := dec.Decode(&doc); !errors.Is(err, io.EOF) {
		t.Fatalf("stdout holds more than one JSON document:\n%s", out)
	}
	return decode(t, out).Data.(map[string]any)
}

func TestConfigEditReportsAnUnchangedFile(t *testing.T) {
	for name, edit := range map[string]func(s *sandbox) func([]string){
		"left alone": func(*sandbox) func([]string) { return nil },
		"same bytes written back": func(s *sandbox) func([]string) {
			return func([]string) {
				b, err := os.ReadFile(s.cfgPath)
				if err == nil {
					err = os.WriteFile(s.cfgPath, b, 0o600)
				}
				if err != nil {
					s.t.Error(err)
				}
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			s := newSandbox(t)
			s.writeConfig("")
			s.edit = edit(s)

			code, out, errOut := s.run("config", "edit")
			if code != api.ExitOK {
				t.Fatalf("exit %d %s", code, errOut)
			}
			data := oneEnvelope(t, out)
			if data["valid"] != true || data["changed"] != false || data["path"] != s.cfgPath {
				t.Fatalf("%v", data)
			}
			if strings.Count(errOut, waitHint) != 1 || !strings.HasPrefix(errOut, "hint: ") {
				t.Fatalf("want the hint once on stderr, got %q", errOut)
			}

			s.tty = true
			code, out, errOut = s.run("config", "edit")
			if code != api.ExitOK || out != "no changes to "+s.cfgPath+"; it is valid\n" || strings.Count(errOut, waitHint) != 1 {
				t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
			}
		})
	}
}

func TestConfigEditSavedThroughARenameIsChanged(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("")
	location := "EU"
	s.edit = func([]string) {
		tmp := filepath.Join(filepath.Dir(s.cfgPath), ".config.toml.swp")
		src := "location = \"" + location + "\"\ndata_dir = \"" + filepath.Join(s.dir, "data") + "\"\n"
		if err := os.WriteFile(tmp, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(tmp, s.cfgPath); err != nil {
			t.Fatal(err)
		}
	}
	code, out, errOut := s.run("config", "edit")
	if code != api.ExitOK || errOut != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if data := oneEnvelope(t, out); data["valid"] != true || data["changed"] != true {
		t.Fatalf("%v", data)
	}
	s.tty, location = true, "US"
	if code, out, _ := s.run("config", "edit"); code != api.ExitOK || out != s.cfgPath+" is valid\n" {
		t.Fatalf("a changed file reads as before: exit %d, stdout %q", code, out)
	}
}

func TestConfigEditUnchangedInvalidFileIsAConfigError(t *testing.T) {
	s := newSandbox(t)
	s.writeConfig("nonsense = 1\n")
	code, out, errOut := s.run("config", "edit")
	if code != api.ExitConfig || out != "" {
		t.Fatalf("exit %d, stdout %q", code, out)
	}
	e := errBody(t, errOut)
	if e.Code != "config" || !strings.Contains(e.Message, "line 2") || !strings.Contains(e.Message, "nonsense") {
		t.Fatalf("the error names the key and the line: %+v", e)
	}
	if !strings.Contains(e.Message, "no changes to "+s.cfgPath) || strings.Count(e.Hint, waitHint) != 1 {
		t.Fatalf("the error says the file is unchanged and hints at a waiting editor: %+v", e)
	}
}

func TestConfigEditOfANewFileThatSavesNothing(t *testing.T) {
	s := newSandbox(t)
	code, out, errOut := s.run("config", "edit")
	if code != api.ExitOK {
		t.Fatalf("exit %d %s", code, errOut)
	}
	if !strings.Contains(errOut, "created "+s.cfgPath+" from the template\n") || strings.Count(errOut, waitHint) != 1 {
		t.Fatalf("stderr %q", errOut)
	}
	if data := oneEnvelope(t, out); data["valid"] != true || data["changed"] != false {
		t.Fatalf("%v", data)
	}
}
