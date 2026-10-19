//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"image"
	"net/http"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"gioui.org/io/semantic"
)

func TestNativeMuseOptionalVersionOpensFromPointer(t *testing.T) {
	for _, language := range []string{"en", "es"} {
		for _, tc := range []struct {
			name, release string
			err           error
		}{
			{name: "new-release", release: "1.4.10-R6427.1"},
			{name: "future-format", release: "Muse future custom build"},
			{name: "version-unavailable", err: errors.New("version command unavailable")},
		} {
			t.Run(language+"/"+tc.name, func(t *testing.T) {
				u, recorder, binary, probes := nativeMuseKiloTestUI(t, language)
				u.owner.museVersionProbe = func(path string) (string, error) {
					probes.Add(1)
					if path != binary {
						t.Error("Muse preparation substituted the selected executable", path)
					}
					return tc.release, tc.err
				}
				project := t.TempDir()
				if err := u.uxhUseGlobalFolder(project); err != nil {
					t.Fatal(err)
				}
				before := u.owner.modelLibrary.snapshot()
				h := &nativePointerHarness{t: t, u: u, size: image.Pt(1180, 1500), now: time.Now()}
				for i := 0; i < 3; i++ {
					response := adminRequest(u.owner, "clients/muse", "")
					var info museProfileInfo
					if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &info) != nil || info.Prepared {
						t.Fatal("fresh Muse readiness was not an unprepared profile", response.Code, response.Body.String())
					}
					h.frame()
				}
				if probes.Load() != 0 || recorder.count() != 0 {
					t.Fatal("GET or rendering probed or launched Muse", probes.Load(), recorder.count())
				}
				if state := u.uxhLaunchState("muse"); !state.canOpen || state.reason != "" {
					t.Fatalf("optional version metadata blocked the Open button: %+v", state)
				}
				label := u.tr("Open Muse Code", "Abrir Muse Code")
				h.reveal(label, semantic.Button)
				nativeGridCapture(t, h, "muse-release-"+tc.name+"-"+language+"-before")
				h.click(label, semantic.Button)
				nativeTestWait(t, u, func() bool {
					return u.clientState().Launching == "" && recorder.count() == 1
				})
				if probes.Load() != 1 || !u.clientState().Muse.Info.Prepared {
					t.Fatal("pointer launch did not prepare Muse with optional version metadata", probes.Load(), u.clientState().Muse.Info)
				}
				recorder.mu.Lock()
				plan := recorder.plans[0]
				recorder.mu.Unlock()
				if plan.Client != "muse" || plan.Executable != binary || filepath.Clean(plan.Directory) != filepath.Clean(project) || !reflect.DeepEqual(plan.Env, museKiloEnvironment(museTerminalHome(u.owner.dir))) {
					t.Fatal("pointer launch changed the selected executable, project or private profile", plan)
				}
				for i := 0; i < 3; i++ {
					response := adminRequest(u.owner, "clients/muse", "")
					var info museProfileInfo
					if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &info) != nil || !info.Prepared {
						t.Fatal("prepared Muse readiness lost optional version metadata", response.Code, response.Body.String())
					}
					h.frame()
				}
				if probes.Load() != 1 || recorder.count() != 1 {
					t.Fatal("prepared GET or rendering probed or relaunched Muse", probes.Load(), recorder.count())
				}
				if after := u.owner.modelLibrary.snapshot(); after.Revision != before.Revision || !reflect.DeepEqual(after.Library, before.Library) {
					t.Fatal("pointer launch rewrote the selected shared models")
				}
				nativeGridCapture(t, h, "muse-release-"+tc.name+"-"+language+"-after")
			})
		}
	}
}
