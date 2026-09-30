package provider

import (
	"regexp"
	"strings"
)

// modelwatch.go feeds modelavail.go from real turns: the agent's stdout
// reader shows every line of a grouped-picker CLI (omp/opencode) to a
// modelTurnWatch, which marks the model refused on an access error and
// "last worked" when a turn ends clean. wick's own engine is not watched —
// it resolves and reports models itself.

var (
	reModelField    = regexp.MustCompile(`"model(?:ID)?"\s*:\s*"([^"]+)"`)
	reProviderField = regexp.MustCompile(`"provider(?:ID)?"\s*:\s*"([^"]+)"`)
	rePlanField     = regexp.MustCompile(`"plan_?[tT]ype"\s*:\s*"([^"]+)"`)
)

type modelTurnWatch struct {
	ins    Instance
	model  string // "" until a pin or a stream line names it
	key    string
	failed bool
	// opencode rotation: the provider and account folder this turn runs in
	// (quotaAcct "main" = the instance's own folder).
	quotaProv, quotaAcct string
}

// isErrorLine: only an error frame may count as a quota hit — a model
// answer that merely mentions "429" must not rotate accounts.
func isErrorLine(line string) bool {
	return strings.Contains(line, `"type":"error"`) || strings.Contains(line, `"stopReason":"error"`)
}

// newModelTurnWatch returns nil when ins is not a watched type.
func newModelTurnWatch(ins *Instance, pin string) *modelTurnWatch {
	if ins == nil || ins.Type == TypeWick {
		return nil
	}
	if _, ok := ModelSetsFor(ins.Type); !ok {
		return nil
	}
	w := &modelTurnWatch{ins: *ins}
	if p, ok := ResolvePin(ins, pin); ok {
		w.model, w.key = p.Model, AvailabilityAccount(p.Provider, p.Account)
	} else if pin != "" && !isForeignModelPin(pin) {
		w.model, w.key = pin, modelPrefix(pin)
	}
	if ins.Type == TypeOpencode {
		prov, acct := OpencodeSpawnAccount(*ins, pin)
		if acct == "" {
			acct = OpencodeMainAccount
		}
		w.quotaProv, w.quotaAcct = prov, acct
	}
	return w
}

func modelPrefix(id string) string {
	if i := strings.IndexByte(id, '/'); i > 0 {
		return id[:i]
	}
	return ""
}

// learnModel fills the model from the stream when no pin named it (the CLI
// ran its own default): omp's message "provider"+"model", opencode's
// "providerID"+"modelID".
func (w *modelTurnWatch) learnModel(line string) {
	if w.model != "" {
		return
	}
	m := reModelField.FindStringSubmatch(line)
	if m == nil {
		return
	}
	model := m[1]
	if !strings.Contains(model, "/") {
		if p := reProviderField.FindStringSubmatch(line); p != nil {
			model = p[1] + "/" + model
		}
	}
	w.model, w.key = model, modelPrefix(model)
}

// turnEnded reports a line that closes one turn of the CLI's JSON stream.
func turnEnded(t Type, line string) bool {
	switch t {
	case TypeOMP:
		return strings.Contains(line, `"type":"agent_end"`)
	case TypeOpencode:
		return strings.Contains(line, `"type":"step_finish"`)
	}
	return false
}

func (w *modelTurnWatch) observe(line string) {
	if w == nil {
		return
	}
	w.learnModel(line)
	if IsModelAccessError(line) {
		w.failed = true
		if w.model != "" {
			reason := ""
			if p := rePlanField.FindStringSubmatch(line); p != nil {
				reason = "plan " + p[1]
			}
			MarkModelUnavailable(w.ins, w.key, w.model, reason)
		}
		return
	}
	if w.ins.Type == TypeOpencode && w.quotaProv != "" && isErrorLine(line) && IsQuotaError(line) {
		// Auto moves the next turn to the provider's next account folder.
		MarkAccountExhausted(w.ins, w.quotaProv, w.quotaAcct)
		w.failed = true
		return
	}
	if turnEnded(w.ins.Type, line) {
		if !w.failed && w.model != "" {
			MarkModelWorked(w.ins, w.key, w.model)
			if w.key != "" {
				MarkModelWorked(w.ins, "", w.model) // instance-wide default
			}
		}
		w.failed = false
	}
}

// pickLiveDefault applies the availability rule to an ordered live list
// (operator pin already first): the pin unless refused, else the model that
// last worked on this instance, else the first not refused. "" when the
// list is empty.
func pickLiveDefault(ins Instance, list []ModelSeed) string {
	if len(list) == 0 {
		return ""
	}
	refused := func(id string) bool {
		_, bad := ModelUnavailable(ins, modelPrefix(id), id)
		return bad
	}
	if pin := strings.TrimSpace(ins.LiveModelDefault); pin != "" && list[0].ID == pin && !refused(pin) {
		return pin
	}
	if last := LastWorkedModel(ins, ""); last != "" && !refused(last) {
		for _, m := range list {
			if m.ID == last {
				return last
			}
		}
	}
	for _, m := range list {
		if !refused(m.ID) {
			return m.ID
		}
	}
	return list[0].ID
}

// ProvenDefaultModel is the default for a spawn with no pin, but ONLY when
// wick has evidence (a model that worked or one that was refused): without
// it the CLI's own default stands, unchanged. Peeks the cached live list,
// never execs.
func ProvenDefaultModel(ins Instance) string {
	if !LiveModelsEnabled(ins) || (LastWorkedModel(ins, "") == "" && !anyRefusal(ins)) {
		return ""
	}
	return pickLiveDefault(ins, LiveDefaultFirst(FilterLiveModels(ins, PeekCLIModels(ins)), ins.LiveModelDefault))
}

func anyRefusal(ins Instance) bool {
	modelStateMu.Lock()
	defer modelStateMu.Unlock()
	st, _ := loadModelState(ins)
	for _, e := range st.Accounts {
		if len(e.Unavailable) > 0 {
			return true
		}
	}
	return false
}
