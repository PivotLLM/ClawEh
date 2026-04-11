package cron

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestSaveStore_FilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")

	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("test", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "hello", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	info, err := os.Stat(storePath)
	if err != nil {
		t.Fatalf("Stat failed: %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Errorf("cron store has permission %04o, want 0600", perm)
	}
}

func int64Ptr(v int64) *int64 {
	return &v
}

func TestListJobs_IncludeDisabled_ReturnsCopy(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("job-one", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg1", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	_, err = cs.AddJob("job-two", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg2", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	jobs := cs.ListJobs(true)
	if len(jobs) != 2 {
		t.Fatalf("expected 2 jobs, got %d", len(jobs))
	}

	originalName := jobs[0].Name
	jobs[0].Name = "mutated-name"

	jobs2 := cs.ListJobs(true)
	if jobs2[0].Name != originalName {
		t.Errorf("ListJobs returned a reference to internal data: mutation was visible on second call (got %q, want %q)", jobs2[0].Name, originalName)
	}
}

func TestListJobs_ConcurrentModification(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	cs := NewCronService(storePath, nil)

	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_, _ = cs.AddJob("job", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg", "agent", "cli", "direct", "channel")
		}
	}()

	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			_ = cs.ListJobs(true)
		}
	}()

	wg.Wait()
}

func TestRemoveJob_SaveFailure_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	cs := NewCronService(storePath, nil)

	job, err := cs.AddJob("test-job", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	storeDir := filepath.Dir(storePath)
	if err := os.Chmod(storeDir, 0o444); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	defer func() {
		_ = os.Chmod(storeDir, 0o755)
	}()

	_, removeErr := cs.RemoveJob(job.ID)
	if removeErr == nil {
		t.Error("expected RemoveJob to return an error when the store directory is not writable, got nil")
	}
}

func TestEnableJob_SaveFailure_ReturnsError(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file permission bits are not enforced on Windows")
	}

	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	cs := NewCronService(storePath, nil)

	job, err := cs.AddJob("test-job", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	storeDir := filepath.Dir(storePath)
	if err := os.Chmod(storeDir, 0o444); err != nil {
		t.Fatalf("chmod failed: %v", err)
	}
	defer func() {
		_ = os.Chmod(storeDir, 0o755)
	}()

	_, enableErr := cs.EnableJob(job.ID, false)
	if enableErr == nil {
		t.Error("expected EnableJob to return an error when the store directory is not writable, got nil")
	}
}

func TestCheckJobs_LoadStoreFailure_PreservesJobs(t *testing.T) {
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	cs := NewCronService(storePath, nil)

	_, err := cs.AddJob("job-one", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg1", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}
	_, err = cs.AddJob("job-two", CronSchedule{Kind: "every", EveryMS: int64Ptr(60000)}, "msg2", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob failed: %v", err)
	}

	// Corrupt the store file with invalid JSON.
	if err := os.WriteFile(storePath, []byte("not valid json"), 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	// Reset fileModTime so the mtime check in checkJobs triggers a reload attempt.
	cs.fileModTime = time.Time{}

	// checkJobs is accessible because tests are in package cron (same package).
	// Mark the service as running so checkJobs does not bail out early.
	cs.running = true
	cs.checkJobs()
	cs.running = false

	jobs := cs.ListJobs(true)
	if len(jobs) != 2 {
		t.Errorf("expected 2 jobs after failed reload, got %d (jobs should be preserved on unmarshal error)", len(jobs))
	}
}

// ----------------------------------------------------------------------------
// computeNextRun
// ----------------------------------------------------------------------------

func TestComputeNextRun_At_Future(t *testing.T) {
	cs := newTestService(t)
	future := time.Now().Add(1 * time.Hour).UnixMilli()
	schedule := &CronSchedule{Kind: "at", AtMS: &future}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result == nil {
		t.Fatal("expected non-nil result for future 'at' schedule")
	}
	if *result != future {
		t.Errorf("expected %d, got %d", future, *result)
	}
}

func TestComputeNextRun_At_Past(t *testing.T) {
	cs := newTestService(t)
	past := time.Now().Add(-1 * time.Hour).UnixMilli()
	schedule := &CronSchedule{Kind: "at", AtMS: &past}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result != nil {
		t.Errorf("expected nil for past 'at' schedule, got %d", *result)
	}
}

func TestComputeNextRun_Every(t *testing.T) {
	cs := newTestService(t)
	everyMS := int64(60_000)
	schedule := &CronSchedule{Kind: "every", EveryMS: &everyMS}
	nowMS := time.Now().UnixMilli()

	result := cs.computeNextRun(schedule, nowMS)

	if result == nil {
		t.Fatal("expected non-nil result for 'every' schedule")
	}
	expected := nowMS + everyMS
	if *result != expected {
		t.Errorf("expected %d, got %d", expected, *result)
	}
}

func TestComputeNextRun_Every_ZeroInterval(t *testing.T) {
	cs := newTestService(t)
	zero := int64(0)
	schedule := &CronSchedule{Kind: "every", EveryMS: &zero}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result != nil {
		t.Errorf("expected nil for zero-interval 'every' schedule, got %d", *result)
	}
}

func TestComputeNextRun_Every_NilInterval(t *testing.T) {
	cs := newTestService(t)
	schedule := &CronSchedule{Kind: "every", EveryMS: nil}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result != nil {
		t.Errorf("expected nil for nil-interval 'every' schedule, got %d", *result)
	}
}

func TestComputeNextRun_Cron_Valid(t *testing.T) {
	cs := newTestService(t)
	// "* * * * *" fires every minute.
	schedule := &CronSchedule{Kind: "cron", Expr: "* * * * *"}
	nowMS := time.Now().UnixMilli()

	result := cs.computeNextRun(schedule, nowMS)

	if result == nil {
		t.Fatal("expected non-nil result for valid cron expression")
	}
	// Next tick should be within the next 2 minutes.
	diff := *result - nowMS
	if diff <= 0 || diff > 2*60*1000 {
		t.Errorf("next run %d is not within expected window (nowMS=%d, diff=%dms)", *result, nowMS, diff)
	}
}

func TestComputeNextRun_Cron_Invalid(t *testing.T) {
	cs := newTestService(t)
	schedule := &CronSchedule{Kind: "cron", Expr: "not a valid cron expression!!!"}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result != nil {
		t.Errorf("expected nil for invalid cron expression, got %d", *result)
	}
}

func TestComputeNextRun_UnknownKind(t *testing.T) {
	cs := newTestService(t)
	schedule := &CronSchedule{Kind: "unknown"}

	result := cs.computeNextRun(schedule, time.Now().UnixMilli())

	if result != nil {
		t.Errorf("expected nil for unknown schedule kind, got %d", *result)
	}
}

// ----------------------------------------------------------------------------
// getNextWakeMS
// ----------------------------------------------------------------------------

func TestGetNextWakeMS_NoJobs(t *testing.T) {
	cs := newTestService(t)

	result := cs.getNextWakeMS()

	if result != nil {
		t.Errorf("expected nil for empty job list, got %d", *result)
	}
}

func TestGetNextWakeMS_AllDisabled(t *testing.T) {
	cs := newTestService(t)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Disable the job.
	jobs := cs.ListJobs(true)
	jobs[0].Enabled = false
	if err := cs.UpdateJob(&jobs[0]); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	result := cs.getNextWakeMS()

	if result != nil {
		t.Errorf("expected nil when all jobs are disabled, got %d", *result)
	}
}

func TestGetNextWakeMS_MultipleJobs(t *testing.T) {
	cs := newTestService(t)

	// Add two jobs with known NextRunAtMS values.
	earlyMS := time.Now().Add(30 * time.Second).UnixMilli()
	lateMS := time.Now().Add(90 * time.Second).UnixMilli()

	cs.mu.Lock()
	cs.store.Jobs = []CronJob{
		makeEnabledJobWithNextRun("job-early", earlyMS),
		makeEnabledJobWithNextRun("job-late", lateMS),
	}
	cs.mu.Unlock()

	result := cs.getNextWakeMS()

	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if *result != earlyMS {
		t.Errorf("expected earliest NextRunAtMS %d, got %d", earlyMS, *result)
	}
}

// ----------------------------------------------------------------------------
// executeJobByID
// ----------------------------------------------------------------------------

func TestExecuteJobByID_Success(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	cs.executeJobByID(job.ID)

	if len(called) != 1 || called[0] != job.ID {
		t.Errorf("handler not called with correct job ID: %v", called)
	}

	jobs := cs.ListJobs(true)
	if len(jobs) == 0 {
		t.Fatal("job was unexpectedly removed")
	}
	if jobs[0].State.LastStatus != "ok" {
		t.Errorf("expected LastStatus 'ok', got %q", jobs[0].State.LastStatus)
	}
	if jobs[0].State.LastRunAtMS == nil {
		t.Error("expected LastRunAtMS to be set")
	}
	// "every" schedule should have a new NextRunAtMS.
	if jobs[0].State.NextRunAtMS == nil {
		t.Error("expected NextRunAtMS to be set after successful 'every' execution")
	}
}

func TestExecuteJobByID_HandlerError(t *testing.T) {
	handlerErr := errors.New("handler failure")
	handler := func(job *CronJob) (string, error) {
		return "", handlerErr
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	cs.executeJobByID(job.ID)

	jobs := cs.ListJobs(true)
	if len(jobs) == 0 {
		t.Fatal("job was unexpectedly removed")
	}
	if jobs[0].State.LastStatus != "error" {
		t.Errorf("expected LastStatus 'error', got %q", jobs[0].State.LastStatus)
	}
	if jobs[0].State.LastError != handlerErr.Error() {
		t.Errorf("expected LastError %q, got %q", handlerErr.Error(), jobs[0].State.LastError)
	}
}

func TestExecuteJobByID_DeleteAfterRun(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)

	// "at" schedule sets DeleteAfterRun=true automatically via AddJob.
	futureMS := time.Now().Add(1 * time.Hour).UnixMilli()
	job, err := cs.AddJob("delete-after", CronSchedule{Kind: "at", AtMS: &futureMS}, "msg", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}

	cs.executeJobByID(job.ID)

	if len(called) != 1 {
		t.Errorf("expected handler called once, got %d", len(called))
	}

	jobs := cs.ListJobs(true)
	for _, j := range jobs {
		if j.ID == job.ID {
			t.Errorf("job was not deleted after DeleteAfterRun execution")
		}
	}
}

func TestExecuteJobByID_JobNotFound(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)

	// Should not panic and handler should not be called.
	cs.executeJobByID("nonexistent-id")

	if len(called) != 0 {
		t.Errorf("expected handler not to be called for missing job, called: %v", called)
	}
}

// ----------------------------------------------------------------------------
// checkJobs
// ----------------------------------------------------------------------------

func TestCheckJobs_DueJobExecuted(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Set NextRunAtMS to the past so the job is due.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	cs.running = true
	cs.mu.Unlock()

	cs.checkJobs()

	cs.mu.Lock()
	cs.running = false
	cs.mu.Unlock()

	if len(called) != 1 || called[0] != job.ID {
		t.Errorf("expected handler called once with job ID, got: %v", called)
	}
}

func TestCheckJobs_NotDueJobSkipped(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// NextRunAtMS is already set to future by AddJob (now + 60s).
	cs.mu.Lock()
	cs.running = true
	cs.mu.Unlock()

	cs.checkJobs()

	cs.mu.Lock()
	cs.running = false
	cs.mu.Unlock()

	if len(called) != 0 {
		t.Errorf("expected handler not called for future job, got: %v", called)
	}
}

func TestCheckJobs_DisabledJobSkipped(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Disable the job and set a past NextRunAtMS.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].Enabled = false
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	cs.running = true
	cs.mu.Unlock()

	cs.checkJobs()

	cs.mu.Lock()
	cs.running = false
	cs.mu.Unlock()

	if len(called) != 0 {
		t.Errorf("expected disabled job not to be executed, got: %v", called)
	}
}

func TestCheckJobs_NotRunningBailsEarly(t *testing.T) {
	var called []string
	handler := func(job *CronJob) (string, error) {
		called = append(called, job.ID)
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Force past run time but leave running=false.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 1000
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	cs.mu.Unlock()

	cs.checkJobs() // cs.running is false — should bail immediately.

	if len(called) != 0 {
		t.Errorf("expected no execution when service is not running, got: %v", called)
	}
}

// ----------------------------------------------------------------------------
// UpdateJob
// ----------------------------------------------------------------------------

func TestUpdateJob_Success(t *testing.T) {
	cs := newTestService(t)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	job.Name = "updated-name"
	if err := cs.UpdateJob(job); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	jobs := cs.ListJobs(true)
	if len(jobs) == 0 {
		t.Fatal("no jobs after update")
	}
	if jobs[0].Name != "updated-name" {
		t.Errorf("expected name 'updated-name', got %q", jobs[0].Name)
	}
	if jobs[0].UpdatedAtMS == 0 {
		t.Error("expected UpdatedAtMS to be set")
	}
}

func TestUpdateJob_NotFound(t *testing.T) {
	cs := newTestService(t)

	phantom := &CronJob{ID: "does-not-exist", Name: "ghost"}
	err := cs.UpdateJob(phantom)

	if err == nil {
		t.Error("expected error for UpdateJob on unknown ID, got nil")
	}
}

// ----------------------------------------------------------------------------
// Start / Stop lifecycle
// ----------------------------------------------------------------------------

func TestStartStop_Lifecycle(t *testing.T) {
	cs := newTestService(t)

	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cs.mu.RLock()
	running := cs.running
	cs.mu.RUnlock()

	if !running {
		t.Error("expected service to be running after Start")
	}

	cs.Stop()

	cs.mu.RLock()
	running = cs.running
	cs.mu.RUnlock()

	if running {
		t.Error("expected service to be stopped after Stop")
	}
}

func TestStart_IdempotentWhenAlreadyRunning(t *testing.T) {
	cs := newTestService(t)

	if err := cs.Start(); err != nil {
		t.Fatalf("first Start: %v", err)
	}
	defer cs.Stop()

	// Second Start should be a no-op and return nil.
	if err := cs.Start(); err != nil {
		t.Errorf("second Start returned unexpected error: %v", err)
	}
}

func TestStop_IdempotentWhenNotRunning(t *testing.T) {
	cs := newTestService(t)
	// Should not panic.
	cs.Stop()
	cs.Stop()
}

func TestStartStop_JobExecutedDuringRun(t *testing.T) {
	executed := make(chan string, 1)
	handler := func(job *CronJob) (string, error) {
		executed <- job.ID
		return "ok", nil
	}

	cs := newTestServiceWithHandler(t, handler)
	job := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Start the service (recomputeNextRuns will set future NextRunAtMS).
	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cs.Stop()

	// After Start() returns the goroutine is running but the ticker hasn't
	// fired yet (1-second interval). Override NextRunAtMS to the past now so
	// the very next tick sees the job as due.
	cs.mu.Lock()
	pastMS := time.Now().UnixMilli() - 500
	for i := range cs.store.Jobs {
		if cs.store.Jobs[i].ID == job.ID {
			cs.store.Jobs[i].State.NextRunAtMS = &pastMS
		}
	}
	cs.mu.Unlock()

	select {
	case id := <-executed:
		if id != job.ID {
			t.Errorf("unexpected job ID executed: %q", id)
		}
	case <-time.After(4 * time.Second):
		t.Error("timed out waiting for job to execute")
	}
}

// ----------------------------------------------------------------------------
// recomputeNextRuns
// ----------------------------------------------------------------------------

func TestRecomputeNextRuns_SetsNextRunForEnabledJobs(t *testing.T) {
	cs := newTestService(t)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Clear NextRunAtMS manually.
	cs.mu.Lock()
	for i := range cs.store.Jobs {
		cs.store.Jobs[i].State.NextRunAtMS = nil
	}
	cs.recomputeNextRuns()
	cs.mu.Unlock()

	jobs := cs.ListJobs(true)
	if jobs[0].State.NextRunAtMS == nil {
		t.Error("expected NextRunAtMS to be set after recomputeNextRuns")
	}
}

func TestRecomputeNextRuns_SkipsDisabledJobs(t *testing.T) {
	cs := newTestService(t)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))

	cs.mu.Lock()
	for i := range cs.store.Jobs {
		cs.store.Jobs[i].Enabled = false
		cs.store.Jobs[i].State.NextRunAtMS = nil
	}
	cs.recomputeNextRuns()
	cs.mu.Unlock()

	jobs := cs.ListJobs(true)
	if jobs[0].State.NextRunAtMS != nil {
		t.Error("expected NextRunAtMS to remain nil for disabled job after recomputeNextRuns")
	}
}

// ----------------------------------------------------------------------------
// ListJobs filtering
// ----------------------------------------------------------------------------

func TestListJobs_FilterDisabled(t *testing.T) {
	cs := newTestService(t)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))
	j2 := addEnabledJob(t, cs, "every", int64Ptr(60_000))

	// Disable j2.
	j2.Enabled = false
	if err := cs.UpdateJob(j2); err != nil {
		t.Fatalf("UpdateJob: %v", err)
	}

	enabled := cs.ListJobs(false)
	if len(enabled) != 1 {
		t.Errorf("expected 1 enabled job, got %d", len(enabled))
	}

	all := cs.ListJobs(true)
	if len(all) != 2 {
		t.Errorf("expected 2 total jobs, got %d", len(all))
	}
}

// ----------------------------------------------------------------------------
// Status
// ----------------------------------------------------------------------------

func TestStatus(t *testing.T) {
	cs := newTestService(t)
	addEnabledJob(t, cs, "every", int64Ptr(60_000))
	addEnabledJob(t, cs, "every", int64Ptr(60_000))

	status := cs.Status()

	if status["jobs"] != 2 {
		t.Errorf("expected jobs=2, got %v", status["jobs"])
	}
	if status["enabled"] != false {
		t.Errorf("expected enabled=false before Start, got %v", status["enabled"])
	}

	if err := cs.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer cs.Stop()

	status = cs.Status()
	if status["enabled"] != true {
		t.Errorf("expected enabled=true after Start, got %v", status["enabled"])
	}
}

func TestStatus_NextWakeAtMS(t *testing.T) {
	cs := newTestService(t)

	// No jobs — nextWakeAtMS should be a nil *int64.
	status := cs.Status()
	// A nil *int64 stored in map[string]any compares as non-nil interface;
	// type-assert to confirm the underlying pointer is nil.
	if v, ok := status["nextWakeAtMS"].(*int64); ok && v != nil {
		t.Errorf("expected nil *int64 nextWakeAtMS with no jobs, got %d", *v)
	}

	addEnabledJob(t, cs, "every", int64Ptr(60_000))
	status = cs.Status()
	v, ok := status["nextWakeAtMS"].(*int64)
	if !ok || v == nil {
		t.Error("expected non-nil *int64 nextWakeAtMS after adding an enabled job")
	}
}

// ----------------------------------------------------------------------------
// helpers
// ----------------------------------------------------------------------------

// newTestService returns a CronService backed by a temp directory.
func newTestService(t *testing.T) *CronService {
	t.Helper()
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	return NewCronService(storePath, nil)
}

// newTestServiceWithHandler returns a CronService with the given handler.
func newTestServiceWithHandler(t *testing.T, handler JobHandler) *CronService {
	t.Helper()
	tmpDir := t.TempDir()
	storePath := filepath.Join(tmpDir, "cron", "jobs.json")
	return NewCronService(storePath, handler)
}

// addEnabledJob adds an enabled job with the given schedule kind and returns it.
// kind must be "every"; everyMS is the interval pointer (used only for "every").
func addEnabledJob(t *testing.T, cs *CronService, kind string, everyMS *int64) *CronJob {
	t.Helper()
	schedule := CronSchedule{Kind: kind, EveryMS: everyMS}
	job, err := cs.AddJob("test-job", schedule, "hello", "agent", "cli", "direct", "channel")
	if err != nil {
		t.Fatalf("AddJob: %v", err)
	}
	return job
}

// makeEnabledJobWithNextRun creates an in-memory CronJob with a preset NextRunAtMS.
func makeEnabledJobWithNextRun(id string, nextRunAtMS int64) CronJob {
	return CronJob{
		ID:      id,
		Name:    id,
		Enabled: true,
		Schedule: CronSchedule{
			Kind:    "every",
			EveryMS: int64Ptr(60_000),
		},
		State: CronJobState{
			NextRunAtMS: &nextRunAtMS,
		},
	}
}
