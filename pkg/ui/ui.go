package ui

import (
	"fmt"
	"net/url"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/Raunak0000/Hydra/pkg/models"
	"github.com/Raunak0000/Hydra/pkg/storage"
)

type UIApp struct {
	window        fyne.Window
	jobList       *widget.List
	jobs          []models.UIJob
	activeFilter  string
	selectedJobID string

	// Inspector widgets
	lblFilename *widget.Label
	lblSavePath *widget.Label
	lblETA      *widget.Label
	lblURL      *widget.Hyperlink
}

func NewUIApp(w fyne.Window) *UIApp {
	return &UIApp{
		window:       w,
		activeFilter: "ALL",
		jobs:         []models.UIJob{},
	}
}

func (ui *UIApp) BuildUI(executeTrigger func(string, string, string, map[string]string)) fyne.CanvasObject {
	// 1. Sidebar Filter Buttons
	filterList := widget.NewList(
		func() int { return 5 },
		func() fyne.CanvasObject { return widget.NewLabel("Filter Item") },
		func(i widget.ListItemID, o fyne.CanvasObject) {
			filters := []string{"All Tasks", "Active", "Scheduled", "Paused", "Completed"}
			o.(*widget.Label).SetText(filters[i])
		},
	)

	filterList.OnSelected = func(i widget.ListItemID) {
		filters := []string{"ALL", "DOWNLOADING", "SCHEDULED", "PAUSED", "COMPLETED"}
		ui.activeFilter = filters[i]
		ui.RefreshJobs()
	}

	// 2. Download Queue List Widget
	ui.jobList = widget.NewList(
		func() int { return len(ui.jobs) },
		func() fyne.CanvasObject {
			return container.NewHBox(
				widget.NewLabel("File Name Placeholder"),
				widget.NewProgressBar(),
			)
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			if i >= len(ui.jobs) {
				return
			}
			job := ui.jobs[i]
			box := o.(*fyne.Container)
			if len(box.Objects) >= 2 {
				if lbl, ok := box.Objects[0].(*widget.Label); ok {
					lbl.SetText(fmt.Sprintf("%s [%.1f%%] - %s", job.FileName, job.Progress, job.Status))
				}
			}
		},
	)

	ui.jobList.OnSelected = func(i widget.ListItemID) {
		if i < len(ui.jobs) {
			job := ui.jobs[i]
			ui.selectedJobID = job.ID
			ui.lblFilename.SetText(job.FileName)
			ui.lblSavePath.SetText(job.SavePath)
			ui.lblETA.SetText(job.ETA)
			if parsedURL, err := url.Parse(job.URL); err == nil {
				ui.lblURL.SetURL(parsedURL)
				ui.lblURL.SetText(job.URL)
			}
		}
	}

	// 3. Top Toolbar / Action Buttons
	btnNewTask := widget.NewButton("New Task", func() {
		ui.showNewTaskDialog(executeTrigger)
	})

	btnPause := widget.NewButton("Pause", func() {
		if ui.selectedJobID == "" {
			return
		}
		db, err := storage.GetDBStore()
		if err == nil {
			_ = db.UpdateStatus(ui.selectedJobID, "PAUSED")
			ui.RefreshJobs()
		}
	})

	btnDelete := widget.NewButton("Delete", func() {
		if ui.selectedJobID == "" {
			return
		}
		db, err := storage.GetDBStore()
		if err == nil {
			_ = db.DeleteJob(ui.selectedJobID)
			ui.selectedJobID = ""
			ui.RefreshJobs()
		}
	})

	toolbar := container.NewHBox(btnNewTask, btnPause, btnDelete)

	// 4. Bottom Inspector Panel
	ui.lblFilename = widget.NewLabel("N/A")
	ui.lblSavePath = widget.NewLabel("N/A")
	ui.lblETA = widget.NewLabel("--")
	ui.lblURL = widget.NewHyperlink("N/A", nil)

	inspectorGrid := container.NewGridWithColumns(2,
		widget.NewLabel("File Name:"), ui.lblFilename,
		widget.NewLabel("Destination:"), ui.lblSavePath,
		widget.NewLabel("ETA:"), ui.lblETA,
		widget.NewLabel("URL:"), ui.lblURL,
	)

	inspectorContainer := container.NewVBox(
		widget.NewLabelWithStyle("Task Inspector", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		inspectorGrid,
	)

	// 5. Layout Composition
	splitCenter := container.NewHSplit(filterList, ui.jobList)
	splitCenter.SetOffset(0.25)

	mainSplit := container.NewVSplit(splitCenter, inspectorContainer)
	mainSplit.SetOffset(0.7)

	rootContainer := container.NewBorder(toolbar, nil, nil, nil, mainSplit)

	// Start background refresh ticker for SQLite state
	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for range ticker.C {
			ui.RefreshJobs()
		}
	}()

	return rootContainer
}

func (ui *UIApp) RefreshJobs() {
	db, err := storage.GetDBStore()
	if err != nil {
		return
	}
	allJobs := db.GetAllJobs()

	var filtered []models.UIJob
	for _, j := range allJobs {
		if ui.activeFilter == "ALL" || j.Status == ui.activeFilter {
			filtered = append(filtered, j)
		}
	}

	ui.jobs = filtered
	if ui.jobList != nil {
		ui.jobList.Refresh()
	}
}

func (ui *UIApp) showNewTaskDialog(executeTrigger func(string, string, string, map[string]string)) {
	urlEntry := widget.NewEntry()
	urlEntry.SetPlaceHolder("https://example.com/file.iso")

	pathEntry := widget.NewEntry()
	pathEntry.SetText(storage.GetDefaultDownloadsDir() + "/")

	formItems := []*widget.FormItem{
		widget.NewFormItem("Source URL", urlEntry),
		widget.NewFormItem("Save Path", pathEntry),
	}

	dialog.ShowForm("New Download Task", "Download", "Cancel", formItems, func(ok bool) {
		if !ok || urlEntry.Text == "" {
			return
		}

		targetURL := urlEntry.Text
		savePath, err := storage.ResolvePath(pathEntry.Text)
		if err != nil {
			dialog.ShowError(err, ui.window)
			return
		}

		jobID := fmt.Sprintf("job_%d", time.Now().UnixNano())
		fileName := storage.TruncateFilename(pathEntry.Text, 120)

		newJob := models.UIJob{
			ID:         jobID,
			FileName:   fileName,
			URL:        targetURL,
			SavePath:   savePath,
			Progress:   0.0,
			TotalSize:  "Calculating...",
			Downloaded: "0.00 MB",
			Speed:      "0.00 KB/s",
			Status:     "DOWNLOADING",
		}

		db, err := storage.GetDBStore()
		if err == nil {
			_ = db.SaveJob(&newJob)
			ui.RefreshJobs()
			go executeTrigger(targetURL, savePath, jobID, nil)
		}
	}, ui.window)
}
