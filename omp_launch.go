package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
)

func (a *app) applyOMPLaunch(p *clientLaunchPlan) error {
	root, dir, err := a.ompPaths()
	fail := profileLaunchError("Oh My Pi")
	if err != nil || !safeLaunchDir(dir, root) {
		return fail
	}
	data, err := readCatalogFile(filepath.Join(dir, "kilo-models.json"))
	var s ompSelection
	if err != nil || json.Unmarshal(data, &s) != nil || validateOMPSelection(s) != nil {
		return fail
	}
	files, err := a.ompProfileFiles(dir, s)
	if err != nil {
		return fail
	}
	for _, f := range files {
		if !f.exists {
			return fail
		}
		if filepath.Ext(f.path) == ".yml" {
			if !equalOMPYAML(f.old, f.new) {
				return fail
			}
		} else if !launchEqualJSON(f.old, f.new) {
			return fail
		}
	}
	p.Env["PI_CODING_AGENT_DIR"] = dir
	// Named profiles take priority over PI_CODING_AGENT_DIR. Override both
	// inherited selectors; stateful Responses is unsupported by Kilo.
	p.Env["OMP_PROFILE"], p.Env["PI_PROFILE"] = "", ""
	p.Env["PI_OPENAI_STATEFUL"] = "0"
	p.Args = []string{"--model", "kilo-local/" + s.Initial}
	return nil
}

func ompLaunchCommand(dir, initial, shell string) string {
	if !helperCommandValue(dir) || !catalogID.MatchString(initial) {
		return ""
	}
	if shell == "powershell" {
		return fmt.Sprintf(`& {
  $kiloProfile = %s
  if (!(Test-Path -LiteralPath (Join-Path $kiloProfile 'models.yml') -PathType Leaf)) { throw 'Prepare Oh My Pi first.' }
  $kiloNames = @('PI_CODING_AGENT_DIR', 'OMP_PROFILE', 'PI_PROFILE', 'PI_OPENAI_STATEFUL')
  $kiloPrevious = @{}
  foreach ($kiloName in $kiloNames) { $kiloPrevious[$kiloName] = [Environment]::GetEnvironmentVariable($kiloName, 'Process') }
  try {
    $env:PI_CODING_AGENT_DIR = $kiloProfile
    $env:OMP_PROFILE = ''
    $env:PI_PROFILE = ''
    $env:PI_OPENAI_STATEFUL = '0'
    omp --model %s
  } finally {
    foreach ($kiloName in $kiloNames) {
      if ($null -eq $kiloPrevious[$kiloName]) { Remove-Item -LiteralPath "Env:$kiloName" -ErrorAction SilentlyContinue }
      else { Set-Item -LiteralPath "Env:$kiloName" -Value $kiloPrevious[$kiloName] }
    }
  }
}`, helperPowerShellQuote(dir), helperPowerShellQuote("kilo-local/"+initial))
	}
	return fmt.Sprintf("(\n  kilo_profile=%s\n  [ -f \"$kilo_profile/models.yml\" ] || { printf '%%s\\n' 'Prepare Oh My Pi first.' >&2; exit 1; }\n  PI_CODING_AGENT_DIR=\"$kilo_profile\" OMP_PROFILE='' PI_PROFILE='' PI_OPENAI_STATEFUL=0 omp --model %s\n)", helperShellQuote(dir), helperShellQuote("kilo-local/"+initial))
}
