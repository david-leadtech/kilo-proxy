package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func t3CodeMuseTestRuntimePolicy() t3CodeMuseRuntimePolicy {
	return t3CodeMuseRuntimePolicy{Levels: map[string][]string{"vendor/one": {"low", "high"}, "vendor/nested/two": {}}, Defaults: map[string]string{"vendor/one": "low", "vendor/nested/two": ""}}
}

func TestT3CodeMuseRuntimeFiltersOnlyManagedReasoningAndDefaults(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("Node is required for JavaScript behavioral fixture")
	}
	policy := map[string][]string{t3CodeMuseProxyID: {"vendor/one", "vendor/nested/two"}}
	script := string(t3CodeRuntimeModelFilterWithMuse(policy, map[string]string{t3CodeMuseProxyID: "vendor/nested/two"}, nil, t3CodeMuseTestRuntimePolicy())) + `
const agent={id:"agent",type:"select",options:[{id:"build"}]};
const reasoning={id:"reasoningEffort",type:"select",currentValue:"high",options:[{id:"low"},{id:"high",isDefault:true},{id:"ultra"}]};
const models=[{slug:"vendor/one",isDefault:true,capabilities:{optionDescriptors:[reasoning,agent]}},{slug:"vendor/nested/two",capabilities:{optionDescriptors:[reasoning,agent]}},{slug:"not/selected"}];
const before=JSON.stringify(models);
const filtered=__kiloT3InstanceModels("kilo_muse_proxy","muse",models);
if(filtered.length!==2||filtered[0].isDefault!==false||filtered[1].isDefault!==true)throw Error("exact model/default policy lost");
const descriptor=filtered[0].capabilities.optionDescriptors[0];
if(descriptor.currentValue!=="low"||descriptor.options.length!==2||descriptor.options[0].isDefault!==true||Object.hasOwn(descriptor.options[1],"isDefault"))throw Error("saved Muse low default or exact levels lost");
if(filtered[0].capabilities.optionDescriptors[1]!==agent||filtered[1].capabilities.optionDescriptors.length!==1||filtered[1].capabilities.optionDescriptors[0]!==agent)throw Error("agent lost or reasoning invented for empty model");
if(__kiloT3InstanceModels("muse","muse",models)!==models||__kiloT3InstanceModels("kilo_muse_proxy","other",models)!==models)throw Error("native or unrelated account modified");
if(JSON.stringify(models)!==before)throw Error("native capabilities mutated");
`
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--input-type=module", "--eval", script)
	cmd.Env = []string{"HOME=" + t.TempDir(), "PATH=" + filepath.Dir(node)}
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, output)
	}
}

func TestT3CodeMuseRuntimeIntegrityAndPolicyIdentity(t *testing.T) {
	binary, root, old := t3CodeRuntimeSynthetic(t, "macos")
	allow := map[string][]string{t3CodeMuseProxyID: {"vendor/one", "vendor/nested/two"}}
	defaults := map[string]string{t3CodeMuseProxyID: "vendor/one"}
	muse := t3CodeMuseTestRuntimePolicy()
	record, err := prepareT3CodeRuntimeWithMuse(root, binary, "macos", allow, defaults, nil, muse)
	if err != nil {
		t.Fatal(err)
	}
	if !validT3CodeRuntimeRoot(record, root) || !t3CodeRuntimeReady(record, binary, "macos") {
		t.Fatal("Muse runtime not ready")
	}
	if record.Root == old.Root || !reflect.DeepEqual(record.MuseReasoningLevels, muse.Levels) || !reflect.DeepEqual(record.MuseReasoningDefaults, muse.Defaults) {
		t.Fatal("Muse policy missing from runtime")
	}
	changed := t3CodeMuseTestRuntimePolicy()
	changed.Defaults["vendor/one"] = "high"
	next, err := prepareT3CodeRuntimeWithMuse(root, binary, "macos", allow, defaults, nil, changed)
	if err != nil || next.Root == record.Root {
		t.Fatal("changed default reused stale private assets", err)
	}
	copy := record
	copy.MuseReasoningDefaults = changed.Defaults
	if validT3CodeRuntimeRoot(copy, root) {
		t.Fatal("tampered Muse policy accepted")
	}
	before, err := os.ReadFile(record.SourceArchive)
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []t3CodeMuseRuntimePolicy{
		{}, {Levels: muse.Levels, Defaults: map[string]string{"vendor/one": "ultra", "vendor/nested/two": ""}},
		{Levels: map[string][]string{"vendor/one": {"low", "low"}, "vendor/nested/two": {}}, Defaults: muse.Defaults},
		{Levels: map[string][]string{"vendor/one": {"low", "high"}, "not/selected": {}}, Defaults: muse.Defaults},
		{Levels: muse.Levels, Defaults: map[string]string{"vendor/one": "low", "vendor/nested/two": "high"}},
	} {
		if _, err := prepareT3CodeRuntimeWithMuse(root, binary, "macos", allow, defaults, nil, invalid); err == nil {
			t.Fatal("invalid reasoning/default policy accepted")
		}
	}
	after, err := os.ReadFile(record.SourceArchive)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("original package modified")
	}
}

func TestT3CodeMuseDefaultSelectionSanitizesOnlyStaleReasoning(t *testing.T) {
	policy := t3CodeModelPolicy{MuseLibrary: modelLibrary{SchemaVersion: 1, DefaultModel: "vendor/one", Models: []modelLibraryItem{{ID: "vendor/one"}, {ID: "vendor/nested/two"}}}, MuseReasoningLevels: t3CodeMuseTestRuntimePolicy().Levels, MuseReasoningDefaults: t3CodeMuseTestRuntimePolicy().Defaults}
	providers := map[string]any{t3CodeMuseProxyID: map[string]any{"enabled": true}, "muse": map[string]any{"enabled": true}}
	want := map[string]any{"instanceId": t3CodeMuseProxyID, "model": "vendor/one", "options": []any{map[string]any{"id": "reasoningEffort", "value": "low"}}}
	if got := t3CodeDefaultSelection(nil, providers, policy); !reflect.DeepEqual(got, want) {
		t.Fatal("Muse-only default incorrect", got)
	}
	for _, options := range []any{map[string]any{"reasoningEffort": "ultra", "agent": "build"}, []any{map[string]any{"id": "reasoningEffort", "value": "ultra"}, map[string]any{"id": "agent", "value": "build"}}} {
		selected := map[string]any{"instanceId": t3CodeMuseProxyID, "model": "vendor/one", "options": options}
		before, _ := json.Marshal(selected)
		actual := t3CodeDefaultSelection(selected, providers, policy)
		data, _ := json.Marshal(actual)
		if bytes.Contains(data, []byte("ultra")) || !bytes.Contains(data, []byte("build")) || !bytes.Contains(data, []byte("low")) {
			t.Fatal("stale option retained or other option deleted", string(data))
		}
		after, _ := json.Marshal(selected)
		if !bytes.Equal(before, after) {
			t.Fatal("original selection mutated")
		}
	}
	native := map[string]any{"instanceId": "muse", "model": "native/model", "options": map[string]any{"reasoningEffort": "ultra"}}
	if got := t3CodeDefaultSelection(native, providers, policy); !reflect.DeepEqual(got, native) {
		t.Fatal("native Muse selection modified")
	}
	valid := map[string]any{"instanceId": t3CodeMuseProxyID, "model": "vendor/one", "options": map[string]any{"reasoningEffort": "high"}}
	if got := t3CodeDefaultSelection(valid, providers, policy); !reflect.DeepEqual(got, valid) {
		t.Fatal("valid remembered effort removed")
	}
}
