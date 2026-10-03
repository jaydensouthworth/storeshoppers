package shop

import (
	"database/sql"
	"fmt"
)

// Departments use current catalog metadata in stable order. Exceptional lines
// remain visible in a separate, first-class lane and are never silently picked.
type HandheldDepartment struct {
	Name  string
	Lines []HandheldLine
	Done  int64
}
type HandheldSaved struct {
	LineID, Version, PickVersion, LineDelta int64
	Key                                     string
	Scan                                    HandheldScan
	BeforePicked                            int64
	Counted, Stay                           bool
}
type HandheldUndo struct {
	HandheldPick
	Name string
}

func completeDelta(before, after bool) int64 {
	if before == after {
		return 0
	}
	if after {
		return 1
	}
	return -1
}
func handheldExceptionsTx(tx *sql.Tx, task *HandheldTask, g handheldGrant, now int64) error {
	for i := range task.Lines {
		line := &task.Lines[i]
		if line.Archived {
			line.Exception = "Archived product · manager review"
			continue
		}
		if line.Unavailable > 0 || line.Cancelled > 0 {
			line.Exception = "Quantity needs manager review"
			continue
		}
		// Only current source-line versions need attention; an explicit later pick
		// or correction is a new physical confirmation and clears this display cue.
		p, err := scanSubstitution(tx.QueryRow(`SELECT `+substitutionColumns+` FROM substitution_proposals WHERE order_id=? AND line_id=? AND epoch=? AND json_extract(snapshot,'$.Command.PickVersion')=? ORDER BY id DESC LIMIT 1`, g.OrderID, line.LineID, g.Epoch, line.PickVersion))
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		if err == nil {
			switch p.Status {
			case "pending":
				if e := currentProposalTx(tx, p, now); e == nil {
					line.Exception = "Replacement awaiting customer"
				} else if substitutionProblem(e) {
					line.Exception = "Replacement needs fresh review"
				} else {
					return e
				}
			case "rejected":
				line.Exception = "Replacement declined · choose next step"
			case "stale":
				line.Exception = "Replacement needs fresh review"
			}
		}
		if line.Exception == "" && task.Held {
			var reported bool
			err = tx.QueryRow(`SELECT EXISTS(SELECT 1 FROM handheld_pick_commands c JOIN handheld_grants h ON h.id=c.grant_id WHERE h.assignment_id=? AND c.line_id=? AND c.pick_version=? AND c.command_key LIKE 'handheld-report:%')`, g.AssignmentID, line.LineID, line.PickVersion).Scan(&reported)
			if err != nil {
				return err
			}
			if reported {
				line.Exception = "Item reported · manager review"
			}
		}
	}
	return nil
}
func groupHandheldTask(task *HandheldTask) {
	task.Departments = nil
	task.Exceptions = nil
	for _, line := range task.Lines {
		if line.Department == "" {
			line.Department = "Other department"
		}
		if line.Exception != "" && !line.Complete() {
			task.Exceptions = append(task.Exceptions, line)
			continue
		}
		index := -1
		for i := range task.Departments {
			if task.Departments[i].Name == line.Department {
				index = i
				break
			}
		}
		if index < 0 {
			task.Departments = append(task.Departments, HandheldDepartment{Name: line.Department})
			index = len(task.Departments) - 1
		}
		task.Departments[index].Lines = append(task.Departments[index].Lines, line)
		if line.Complete() {
			task.Departments[index].Done++
		}
	}
}
func nextHandheldLine(task *HandheldTask, after int64) *HandheldLine {
	start := -1
	for i := range task.Lines {
		if task.Lines[i].LineID == after {
			start = i
			break
		}
	}
	for n := 1; n <= len(task.Lines); n++ {
		i := (start + n) % len(task.Lines)
		line := &task.Lines[i]
		if line.LineID != after && !line.Complete() && !line.Archived && line.Exception == "" {
			return line
		}
	}
	return nil
}

// This is a projection of a committed command, never a client-side guess.
// Concurrent edits or replayed commands do not move selection or mint undo.
func applyHandheldFlow(v *HandheldView) {
	if v.Task == nil || !v.Task.Employee {
		return
	}
	after := int64(0)
	if v.Selected != nil {
		after = v.Selected.LineID
	}
	v.NextLine = nextHandheldLine(v.Task, after)
	saved := v.Saved
	if saved == nil || v.Task.Version != saved.Version || v.Task.Assignment.Version != saved.Scan.AssignmentVersion {
		return
	}
	var line *HandheldLine
	for i := range v.Task.Lines {
		if v.Task.Lines[i].LineID == saved.LineID {
			line = &v.Task.Lines[i]
			break
		}
	}
	if line == nil || line.PickVersion != saved.PickVersion {
		return
	}
	v.LastSavedLine = line
	v.PickEvent = saved.Key
	v.PickLineDelta = saved.LineDelta
	if saved.Counted {
		scan := saved.Scan
		scan.Version = saved.Version
		scan.PickVersion = saved.PickVersion
		if line.Picked != saved.BeforePicked && !saved.Stay {
			v.Undo = &HandheldUndo{HandheldPick: HandheldPick{HandheldScan: scan, Picked: saved.BeforePicked, Key: token()}, Name: line.Name}
		}
		// Keep the verified code for another explicit absolute-count confirmation.
		// No scan or request automatically increments a quantity.
		if !line.Complete() && !line.Archived {
			v.Recognition = &HandheldRecognition{CanPick: true, Code: scan.Code, Source: scan.Source, Format: "Code128", State: "recognized", Message: "Code verified. Confirm the total picked when you are ready."}
			v.PickedDraft = fmt.Sprint(min(line.Picked+1, line.Quantity))
		}
	}
	if line.Complete() && !saved.Stay {
		v.Selected = nextHandheldLine(v.Task, line.LineID)
		v.Recognition = nil
		v.WeightReview = nil
		v.WeightDraft = nil
		v.PickedDraft = ""
		v.Advanced = true
	}
}
