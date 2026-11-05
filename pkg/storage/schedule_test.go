package storage

import (
	"testing"
	"time"

	"github.com/Raunak0000/Hydra/pkg/models"
)

func TestScheduleTimezonesAndClock(t *testing.T) {
	store := newTestDBStore(t)

	// Create times in different timezones:
	// 1. Due in UTC+5:30 (e.g. 5 minutes ago)
	ist := time.FixedZone("IST", 5*3600+1800)
	pastIST := time.Now().In(ist).Add(-5 * time.Minute)

	// 2. Due in UTC-4 (e.g. 10 minutes ago)
	edt := time.FixedZone("EDT", -4*3600)
	pastEDT := time.Now().In(edt).Add(-10 * time.Minute)

	// 3. Due in UTC (e.g. 2 minutes ago)
	pastUTC := time.Now().UTC().Add(-2 * time.Minute)

	// 4. Future job in UTC+5:30 (e.g. 1 hour in the future)
	futureIST := time.Now().In(ist).Add(1 * time.Hour)

	// 5. Far positive offset (UTC+14 Kiribati)
	lineIslands := time.FixedZone("LINT", 14*3600)
	pastFarEast := time.Now().In(lineIslands).Add(-3 * time.Minute)

	// 6. Far negative offset (UTC-11 Samoa)
	samoa := time.FixedZone("SST", -11*3600)
	pastFarWest := time.Now().In(samoa).Add(-3 * time.Minute)

	jobs := []models.UIJob{
		{ID: "job-ist-due", FileName: "ist.bin", URL: "https://example.com/ist.bin", SavePath: "/tmp/ist.bin", Status: "SCHEDULED", ScheduledAt: &pastIST},
		{ID: "job-edt-due", FileName: "edt.bin", URL: "https://example.com/edt.bin", SavePath: "/tmp/edt.bin", Status: "SCHEDULED", ScheduledAt: &pastEDT},
		{ID: "job-utc-due", FileName: "utc.bin", URL: "https://example.com/utc.bin", SavePath: "/tmp/utc.bin", Status: "SCHEDULED", ScheduledAt: &pastUTC},
		{ID: "job-fareast-due", FileName: "fe.bin", URL: "https://example.com/fe.bin", SavePath: "/tmp/fe.bin", Status: "SCHEDULED", ScheduledAt: &pastFarEast},
		{ID: "job-farwest-due", FileName: "fw.bin", URL: "https://example.com/fw.bin", SavePath: "/tmp/fw.bin", Status: "SCHEDULED", ScheduledAt: &pastFarWest},
		{ID: "job-future", FileName: "fut.bin", URL: "https://example.com/fut.bin", SavePath: "/tmp/fut.bin", Status: "SCHEDULED", ScheduledAt: &futureIST},
	}

	for i := range jobs {
		if err := store.SaveJob(&jobs[i]); err != nil {
			t.Fatalf("SaveJob failed: %v", err)
		}
	}

	due := store.GetPendingScheduledJobs()
	if len(due) != 5 {
		t.Fatalf("expected 5 due jobs, got %d", len(due))
	}

	dueIDs := make(map[string]bool)
	for _, j := range due {
		dueIDs[j.ID] = true
	}

	for _, expectedID := range []string{"job-ist-due", "job-edt-due", "job-utc-due", "job-fareast-due", "job-farwest-due"} {
		if !dueIDs[expectedID] {
			t.Errorf("expected %s to be due", expectedID)
		}
	}
	if dueIDs["job-future"] {
		t.Errorf("job-future should NOT be due")
	}
}
