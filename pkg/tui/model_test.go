package tui

import (
	"testing"

	"github.com/Raunak0000/Hydra/pkg/models"
	tea "github.com/charmbracelet/bubbletea"
)

func testJobs(count int) []models.UIJob {
	jobs := make([]models.UIJob, count)
	for index := range jobs {
		jobs[index] = models.UIJob{ID: string(rune('a' + index)), FileName: "file"}
	}
	return jobs
}

func loadTestJobs(model Model, jobs []models.UIJob) Model {
	updated, _ := model.Update(jobsLoadedMsg{jobs: jobs})
	return updated.(Model)
}

func TestKeyboardSelectionClampsToJobs(t *testing.T) {
	model := loadTestJobs(NewModel(nil), testJobs(3))

	updated, _ := model.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	updated, _ = updated.Update(tea.KeyMsg{Type: tea.KeyDown})
	if updated.(Model).selected != 2 {
		t.Fatalf("selected = %d, want 2", updated.(Model).selected)
	}
}

func TestMouseClickSelectsVisibleRow(t *testing.T) {
	model := loadTestJobs(NewModel(nil), testJobs(4))
	model.height = 20

	updated, _ := model.Update(tea.MouseMsg(tea.MouseEvent{
		Y: 5, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft,
	}))
	if updated.(Model).selected != 2 {
		t.Fatalf("selected = %d, want 2", updated.(Model).selected)
	}
}

func TestMouseWheelScrollsSelection(t *testing.T) {
	model := loadTestJobs(NewModel(nil), testJobs(20))
	model.height = 10

	updated, _ := model.Update(tea.MouseMsg(tea.MouseEvent{
		Action: tea.MouseActionPress, Button: tea.MouseButtonWheelDown,
	}))
	got := updated.(Model)
	if got.selected != 1 || got.scroll != 0 {
		t.Fatalf("selected=%d scroll=%d, want selected=1 scroll=0", got.selected, got.scroll)
	}
}

func TestDeleteRequiresConfirmation(t *testing.T) {
	model := loadTestJobs(NewModel(nil), testJobs(1))
	updated, command := model.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}})
	got := updated.(Model)
	if !got.confirming {
		t.Fatal("delete did not enter confirmation state")
	}
	if command != nil {
		t.Fatal("delete should not run before confirmation")
	}
}

func TestFooterActionsAreClickable(t *testing.T) {
	model := loadTestJobs(NewModel(nil), testJobs(1))
	model.height = 20
	footerY := model.tableTop() + 1 + 1
	if got := model.footerAction(2, footerY); got != "pause" {
		t.Fatalf("footer pause action = %q", got)
	}
	if got := model.footerAction(15, footerY); got != "resume" {
		t.Fatalf("footer resume action = %q", got)
	}
	if got := model.footerAction(30, footerY); got != "delete" {
		t.Fatalf("footer delete action = %q", got)
	}
}
