package media

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestCanvasArchiveRecoveryDoesNotRegenerateOrCharge(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	for range 16 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	before := p.View()
	calls := 0
	f.s.ArchiveCanvasOutput = func(ctx context.Context, input CanvasArchiveInput) CanvasTeamAssetArchive {
		calls++
		stored, err := f.s.Repo.Get(ctx, input.TeamID, input.ProjectID)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, archive := range stored.TeamAssetArchives {
			if archive.SourceAssetID == input.Source.ID {
				found = true
			}
		}
		if !found || input.UserID != f.user {
			t.Fatalf("archive before durable pending or wrong owner: %+v", input)
		}
		if calls <= 2 {
			return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, Status: "failed", Error: "canvas_archive_storage_limit"}
		}
		return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, Status: "saved", TeamAssetID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(input.Source.ID)).String()}
	}
	f.s.Tick(ctx)
	p = current(t, f, p)
	if len(p.TeamAssetArchives) != 2 || p.TeamAssetArchives[0].Status != "failed" || p.Version <= before.Version || p.Status != before.Status || p.ChargedMicro != before.ChargedMicro || p.QuoteMicro != before.QuoteMicro {
		t.Fatalf("archive failure changed generation or lost projection: %+v", p)
	}
	// worker 重启后从持久化回执恢复，不能依赖进程内 pending 状态。
	f.s.WorkerID = uuid.NewString()
	f.s.Tick(ctx)
	p = current(t, f, p)
	for _, archive := range p.View().TeamAssetArchives {
		if archive.Status != "saved" || archive.TeamAssetID == "" || archive.Error != "" {
			t.Fatalf("archive not recovered: %+v", archive)
		}
	}
	if p.Status != before.Status || p.ChargedMicro != before.ChargedMicro || f.submits.Load() != 2 {
		t.Fatalf("archive regenerated/recharged: %+v calls=%d", p, f.submits.Load())
	}
	version := p.Version
	f.s.Tick(ctx)
	p = current(t, f, p)
	if p.Version != version {
		t.Fatal("identical archive facts churn project version")
	}
}

func TestCanvasArchiveTerminalPartialBackfillAndWire(t *testing.T) {
	for _, status := range []string{"completed", "failed", "cancelled", "reconciliation_required"} {
		t.Run(status, func(t *testing.T) {
			f := setup(t)
			ctx := context.Background()
			r := canvasRequest(t, f)
			p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
			if err != nil {
				t.Fatal(err)
			}
			for range 16 {
				f.s.Tick(ctx)
			}
			p = current(t, f, p)
			p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
				v.Status = status
				v.Shots[1].Status = "failed"
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			f.s.ArchiveCanvasOutput = func(_ context.Context, input CanvasArchiveInput) CanvasTeamAssetArchive {
				calls++
				return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, Status: "deleted", TeamAssetID: uuid.NewString()}
			}
			f.s.Tick(ctx)
			f.s.Tick(ctx)
			p = current(t, f, p)
			if len(p.TeamAssetArchives) != 1 || calls != 1 || p.Status != status {
				t.Fatalf("partial terminal backfill/deleted replay: %+v calls=%d", p, calls)
			}
			wire, err := json.Marshal(p.View())
			if err != nil || !strings.Contains(string(wire), `"teamAssetArchives":[{"sourceAssetId":`) || strings.Contains(string(wire), "leaseOwner") || strings.Contains(string(wire), "userId") {
				t.Fatalf("unsafe archive wire: %s %v", wire, err)
			}
		})
	}
}

func TestCanvasArchiveDelayedAcknowledgementCannotOverwriteNewerFact(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	r := canvasRequest(t, f)
	p, err := f.s.CreateCanvas(ctx, f.team, f.user, "", uuid.NewString(), r)
	if err != nil {
		t.Fatal(err)
	}
	source := uuid.NewString()
	p, err = f.s.Mutate(ctx, f.team, p.ID, p.Version, func(v *Project) error {
		v.Shots[0].Status = "succeeded"
		v.Shots[0].OutputAssetID = source
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	pending := CanvasTeamAssetArchive{SourceAssetID: source, Status: "pending"}
	stored, err := f.s.saveCanvasArchive(ctx, *p, pending, nil)
	if err != nil {
		t.Fatal(err)
	}
	saved := CanvasTeamAssetArchive{SourceAssetID: source, Status: "saved", TeamAssetID: uuid.NewString()}
	stored, err = f.s.saveCanvasArchive(ctx, stored, saved, &pending)
	if err != nil {
		t.Fatal(err)
	}
	version := stored.Version
	late := CanvasTeamAssetArchive{SourceAssetID: source, Status: "failed", Error: "canvas_archive_retrying"}
	stored, err = f.s.saveCanvasArchive(ctx, stored, late, &pending)
	if err != nil || stored.TeamAssetArchives[0] != saved || stored.Version != version {
		t.Fatalf("late failure replaced confirmed save: %+v %v", stored, err)
	}
	deleted := saved
	deleted.Status = "deleted"
	stored, err = f.s.saveCanvasArchive(ctx, stored, deleted, &saved)
	if err != nil {
		t.Fatal(err)
	}
	stored, err = f.s.saveCanvasArchive(ctx, stored, saved, &saved)
	if err != nil || stored.TeamAssetArchives[0].Status != "deleted" {
		t.Fatalf("late save replaced deletion: %+v %v", stored, err)
	}
}

func TestCanvasArchiveProjectionPrunesObsoleteAndDuplicateSources(t *testing.T) {
	p := Project{Canvas: &CanvasRequest{}, Status: "completed"}
	for range 4 {
		id := uuid.NewString()
		p.Shots = append(p.Shots, Shot{Status: "succeeded", OutputAssetID: id})
		p.TeamAssetArchives = append(p.TeamAssetArchives, CanvasTeamAssetArchive{SourceAssetID: id, TeamAssetID: uuid.NewString(), Status: "saved"})
	}
	p.TeamAssetArchives = append(p.TeamAssetArchives, p.TeamAssetArchives[0], CanvasTeamAssetArchive{SourceAssetID: uuid.NewString(), Status: "failed"})
	if len(p.View().TeamAssetArchives) != 4 {
		t.Fatalf("unbounded or duplicate public receipts: %+v", p.View().TeamAssetArchives)
	}
	p.Shots[0].Status = "failed"
	if len(p.View().TeamAssetArchives) != 3 {
		t.Fatal("obsolete failed-shot receipt exposed")
	}
}

func TestGeneratedArchiveSourcesUseOnlyPublicPlatformOutputs(t *testing.T) {
	canvasOutput, canvasTail := uuid.NewString(), uuid.NewString()
	canvas := Project{Canvas: &CanvasRequest{}, Shots: []Shot{{Status: "succeeded", OutputAssetID: canvasOutput, LastFrameAssetID: canvasTail}}}
	if got := generatedArchiveSources(canvas); len(got) != 2 || got[0] != canvasOutput || got[1] != canvasTail {
		t.Fatalf("Canvas outputs: %v", got)
	}

	agentOutput, agentTail := uuid.NewString(), uuid.NewString()
	agent := Project{Agent: &AgentGenerationRequest{}, Shots: []Shot{{Status: "succeeded", OutputAssetID: agentOutput, LastFrameAssetID: agentTail}}}
	if got := generatedArchiveSources(agent); len(got) != 1 || got[0] != agentOutput {
		t.Fatalf("Agent must archive output only: %v", got)
	}

	finalOutput, intermediate, firstFrame := uuid.NewString(), uuid.NewString(), uuid.NewString()
	video := Project{Status: "completed", OutputAssetID: finalOutput, Shots: []Shot{{Status: "succeeded", OutputAssetID: intermediate, ImageAssetID: firstFrame}}}
	if got := generatedArchiveSources(video); len(got) != 1 || got[0] != finalOutput {
		t.Fatalf("video production must archive final output only: %v", got)
	}
	video.Status = "running"
	if got := generatedArchiveSources(video); len(got) != 0 {
		t.Fatalf("unfinished video production archived outputs: %v", got)
	}
}

func TestOrdinaryVideoReconcileArchivesFinalCompositionOnly(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	finalOutput, intermediate := uuid.NewString(), uuid.NewString()
	for _, id := range []string{finalOutput, intermediate} {
		if err := f.s.Repo.AddAsset(ctx, f.team, Asset{ID: id, Name: id, Kind: "video", Mime: "video/mp4"}); err != nil {
			t.Fatal(err)
		}
	}
	p := &Project{ID: uuid.NewString(), TeamID: f.team, UserID: f.user, Status: "completed", Version: 1, OutputAssetID: finalOutput, Shots: []Shot{{Status: "succeeded", OutputAssetID: intermediate}}}
	if err := f.s.Repo.Index(ctx, ProjectKey(f.team, p.ID)); err != nil {
		t.Fatal(err)
	}
	if err := f.s.Repo.Save(ctx, p, nil); err != nil {
		t.Fatal(err)
	}
	var archived []string
	f.s.ArchiveCanvasOutput = func(_ context.Context, input CanvasArchiveInput) CanvasTeamAssetArchive {
		archived = append(archived, input.Source.ID)
		return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, TeamAssetID: uuid.NewString(), Status: "saved"}
	}
	f.s.reconcileCanvasArchives(ctx, *p)
	if len(archived) != 1 || archived[0] != finalOutput {
		t.Fatalf("ordinary video archived intermediate output: %v", archived)
	}
}
