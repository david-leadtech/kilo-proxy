//go:build desktop

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gioui.org/layout"
)

type nativeUsageExplorer struct {
	scope  string
	result *usageQueryResult
}

func (u *nativeUI) usageExplorerState() *nativeUsageExplorer {
	if u.usageExplorer == nil {
		u.usageExplorer = &nativeUsageExplorer{}
		now := time.Now().UTC()
		u.setValue("usage.start", now.AddDate(0, 0, -29).Format("2006-01-02"))
		u.setValue("usage.end", now.Format("2006-01-02"))
		u.setChecked("usage.group.model", true)
		if home, err := os.UserHomeDir(); err == nil {
			u.setValue("usage.folder", filepath.Join(home, "Downloads"))
		}
	}
	scope, _ := u.state["usageAccountScope"].(string)
	if u.usageExplorer.scope != scope {
		u.usageExplorer.scope = scope
		u.usageExplorer.result = nil
	}
	return u.usageExplorer
}

func (u *nativeUI) usageExplorerQuery() usageQuery {
	q := usageQuery{StartDate: strings.TrimSpace(u.value("usage.start")), EndDate: strings.TrimSpace(u.value("usage.end")), GroupBy: []string{}, CostSource: "cost"}
	if u.checked("usage.market") {
		q.CostSource = "market"
	}
	for _, dim := range usageDimensions {
		if u.checked("usage.group." + dim) {
			q.GroupBy = append(q.GroupBy, dim)
		}
	}
	parse := func(id string) []string {
		values := []string{}
		for _, value := range strings.Split(u.value("usage.filter."+id), ",") {
			if value = strings.TrimSpace(value); value != "" {
				values = append(values, value)
			}
		}
		return values
	}
	q.Models, q.Providers, q.Projects, q.Features, q.Modes = parse("models"), parse("providers"), parse("projects"), parse("features"), parse("modes")
	return q
}

func (u *nativeUI) retrieveUsageHistory() {
	s := u.usageExplorerState()
	q := u.usageExplorerQuery()
	if _, _, err := q.validate(time.Now()); err != nil {
		u.noticeError(err)
		return
	}
	s.result = nil
	u.call("POST", "/api/usage/query", q, func(raw json.RawMessage) {
		var result usageQueryResult
		if err := json.Unmarshal(raw, &result); err != nil {
			u.noticeError(err)
			return
		}
		current := u.usageExplorerState()
		scope := u.owner.readUIProjection().UsageScope
		if result.Scope != scope || result.Scope != current.scope {
			return
		}
		current.result = &result
	})
}

func writeUsageCSVFile(dir string, result *usageQueryResult) (string, error) {
	return writeUsageCSVFileAt(dir, result, time.Now())
}

func writeUsageCSVFileAt(dir string, result *usageQueryResult, now time.Time) (string, error) {
	if !filepath.IsAbs(dir) {
		return "", errors.New("Choose an existing CSV destination folder with an absolute path.")
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", errors.New("The CSV destination folder does not exist.")
	}
	data, err := usageQueryCSV(result)
	if err != nil {
		return "", err
	}
	// Exclusive randomized creation also preserves exports made in the same clock tick.
	pattern := strings.TrimSuffix(usageQueryFilename(result.Query), ".csv") + "-" + now.UTC().Format("20060102T150405.000000000") + "-*.csv"
	file, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	path := file.Name()
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err = errors.Join(writeErr, closeErr); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func (u *nativeUI) saveUsageHistoryCSV() {
	s := u.usageExplorerState()
	if s.result == nil {
		return
	}
	scope := u.owner.readUIProjection().UsageScope
	if s.result.Scope != scope {
		s.result = nil
		return
	}
	path, err := writeUsageCSVFile(strings.TrimSpace(u.value("usage.folder")), s.result)
	if err != nil {
		u.noticeError(err)
		return
	}
	u.notice = u.tr("CSV saved: ", "CSV guardado: ") + path
}

func (u *nativeUI) usageExplorerPanel() layout.Widget {
	s := u.usageExplorerState()
	busy := u.busy["POST/api/usage/query"]
	labels := map[string]string{"model": u.tr("Model", "Modelo"), "provider": u.tr("Provider", "Proveedor"), "project": u.tr("Project", "Proyecto"), "feature": u.tr("Feature", "Función"), "mode": u.tr("Mode", "Modo"), "user": u.tr("User", "Usuario")}
	children := []layout.Widget{
		u.heading(u.tr("Historical Kilo usage", "Uso histórico de Kilo")),
		u.note(u.tr("Retrieve your usage from Kilo, including dates before this proxy was installed. Rows are daily aggregates in UTC, grouped by up to three dimensions. Up to 366 days per export.", "Recupera tu uso desde Kilo, incluidas fechas anteriores a la instalación del proxy. Las filas son agregados diarios en UTC, cruzados por hasta tres dimensiones. Hasta 366 días por exportación.")),
		u.topRow(u.disabled(!busy, u.field("usage.start", u.tr("From · UTC", "Desde · UTC"), "YYYY-MM-DD", false)), u.disabled(!busy, u.field("usage.end", u.tr("Through · UTC", "Hasta · UTC"), "YYYY-MM-DD", false))),
		u.disabled(!busy, u.check("usage.market", u.tr("Use market cost instead of Kilo charges", "Usar coste de mercado en lugar de cargos de Kilo"), nil)),
		u.subheading(u.tr("Group each day by · choose up to 3", "Cruzar cada día por · elige hasta 3")),
	}
	count := 0
	for _, dim := range usageDimensions {
		if u.checked("usage.group." + dim) {
			count++
		}
	}
	groups := []layout.Widget{}
	for _, dim := range usageDimensions {
		groups = append(groups, u.disabled(!busy && (count < 3 || u.checked("usage.group."+dim)), u.check("usage.group."+dim, labels[dim], nil)))
	}
	children = append(children, u.topRow(groups[:3]...), u.topRow(groups[3:]...), u.disclosure("usage.filters", u.tr("Filter by exact IDs", "Filtrar por IDs exactos")))
	if u.expanded["usage.filters"] {
		children = append(children, u.note(u.tr("Comma-separated IDs; leave empty for all values. Only your usage in the selected account/team is queried.", "IDs separados por comas; deja vacío para incluir todos. Solo se consulta tu uso de la cuenta o equipo seleccionado.")))
		for _, key := range []string{"models", "providers", "projects", "features", "modes"} {
			children = append(children, u.disabled(!busy, u.field("usage.filter."+key, labels[strings.TrimSuffix(key, "s")], "", false)))
		}
	}
	load := u.tr("Retrieve history", "Recuperar histórico")
	if busy {
		load = u.tr("Retrieving…", "Recuperando…")
	}
	children = append(children, u.disabled(!busy && s.scope != "", u.button("usage.query", load, u.retrieveUsageHistory)))
	if s.scope == "" {
		children = append(children, u.note(u.tr("Sign in to Kilo to retrieve historical usage.", "Inicia sesión en Kilo para recuperar el histórico.")))
	}
	if busy {
		children = append(children, u.note(u.tr("Querying Kilo. Large ranges can take several requests.", "Consultando Kilo. Los rangos amplios pueden necesitar varias consultas.")))
	}
	if s.result != nil {
		r := s.result
		children = append(children, u.note(fmt.Sprintf(u.tr("%d rows · %s through %s · %s · UTC", "%d filas · %s a %s · %s · UTC"), len(r.Rows), r.Query.StartDate, r.Query.EndDate, r.Query.CostSource)), u.note(u.tr("CSV includes every returned row, exact token counts and costMicrodollars (1 USD = 1,000,000). Missing combinations are not filled with zeros.", "El CSV incluye todas las filas devueltas, contadores exactos y costMicrodollars (1 USD = 1.000.000). Las combinaciones ausentes no se rellenan con ceros.")), u.field("usage.folder", u.tr("CSV destination folder", "Carpeta de destino del CSV"), "", false), u.topRow(u.button("usage.choose-folder", u.tr("Choose folder…", "Elegir carpeta…"), func() {
			initial := u.value("usage.folder")
			go func() {
				folder, err := chooseNativeFolder(initial)
				u.enqueue(func() {
					if err == nil {
						u.setValue("usage.folder", folder)
					} else if !errors.Is(err, errNativePickerCancelled) {
						u.noticeError(err)
					}
				})
			}()
		}), u.disabled(!busy, u.button("usage.save-csv", u.tr("Save raw CSV", "Guardar CSV raw"), u.saveUsageHistoryCSV))), u.disclosure("usage.preview", u.tr("Preview · first 100 rows", "Vista previa · primeras 100 filas")))
		if u.expanded["usage.preview"] {
			rows := []layout.Widget{u.note(strings.Join(r.Columns, " · "))}
			for _, row := range r.Rows[:min(100, len(r.Rows))] {
				rows = append(rows, u.note(strings.Join(row, " · ")))
			}
			children = append(children, u.scroll("usage.rows", 280, rows...))
		}
	}
	return u.card(children...)
}
