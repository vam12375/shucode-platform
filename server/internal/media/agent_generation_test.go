package media

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/vam12375/shucode-platform/server/internal/store"
)

func agentGenerationRequest(t *testing.T, f *fixture) AgentGenerationRequest {
	t.Helper()
	_ = canvasRequest(t, f)
	if _, err := f.s.Repo.Store.SaveTeamPolicy(context.Background(), store.TeamPolicyRecord{TeamID: f.team, AllowedModelIDs: []string{"i"}}); err != nil {
		t.Fatal(err)
	}
	return AgentGenerationRequest{
		RequestID: "agent-request-1", Intent: "generate", Kind: "image", Prompt: "portrait",
		AssetIDs: []string{}, ModelID: "i", Resolution: "2K", AspectRatio: "1:1", Count: 1,
	}
}

func TestAgentGenerationCreateIsIdempotentAndRequestBound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	request := agentGenerationRequest(t, f)
	created, err := f.s.CreateAgentGeneration(ctx, f.team, f.user, "device-1", request)
	if err != nil {
		t.Fatal(err)
	}
	again, err := f.s.CreateAgentGeneration(ctx, f.team, f.user, "device-1", request)
	if err != nil || again.ID != created.ID || again.Version != created.Version {
		t.Fatalf("idempotent create: project=%+v err=%v", again, err)
	}
	request.Prompt = "changed"
	if _, err := f.s.CreateAgentGeneration(ctx, f.team, f.user, "device-1", request); !errors.Is(err, ErrConflict) {
		t.Fatalf("changed request reused requestId: %v", err)
	}
	otherUser := uuid.NewString()
	if got, err := f.s.GetAgentGeneration(ctx, f.team, otherUser, "agent-request-1"); err != nil || got != nil {
		t.Fatalf("other user found generation: project=%+v err=%v", got, err)
	}
}

func TestAgentGenerationCancellationIsVersionedAndIdempotent(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	request := agentGenerationRequest(t, f)
	created, err := f.s.CreateAgentGeneration(ctx, f.team, f.user, "device-1", request)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := f.s.CancelAgentGeneration(ctx, f.team, f.user, request.RequestID)
	if err != nil || cancelled == nil || !cancelled.CancelRequested || cancelled.Status != "queued" {
		t.Fatalf("cancellation request should be persisted before worker terminal state: project=%+v err=%v", cancelled, err)
	}
	version := cancelled.Version
	again, err := f.s.CancelAgentGeneration(ctx, f.team, f.user, request.RequestID)
	if err != nil || again.Version != version {
		t.Fatalf("repeated cancellation must be idempotent: project=%+v err=%v", again, err)
	}
	for range 2 {
		f.s.Tick(ctx)
	}
	finished := current(t, f, created)
	if finished.Status != "cancelled" || finished.ChargedMicro != 0 {
		t.Fatalf("queued cancellation should finish without charge: %+v", finished)
	}
}

func TestAgentGenerationWorkerArchivesEveryOutput(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	request := agentGenerationRequest(t, f)
	request.Count = 2
	saved := map[string]bool{}
	f.s.ArchiveCanvasOutput = func(_ context.Context, input CanvasArchiveInput) CanvasTeamAssetArchive {
		saved[input.Source.ID] = true
		return CanvasTeamAssetArchive{SourceAssetID: input.Source.ID, TeamAssetID: uuid.NewString(), Status: "saved"}
	}
	p, err := f.s.CreateAgentGeneration(ctx, f.team, f.user, "device-1", request)
	if err != nil {
		t.Fatal(err)
	}
	for range 30 {
		f.s.Tick(ctx)
	}
	p = current(t, f, p)
	if p.Status != "completed" || len(p.Shots) != 2 || len(saved) != 2 || len(p.TeamAssetArchives) != 2 || AgentGenerationCloudArchive(*p) != "saved" {
		t.Fatalf("Agent outputs were not all archived: project=%+v saved=%v", p, saved)
	}
	for _, shot := range p.Shots {
		if shot.Status != "succeeded" || shot.OutputAssetID == "" || !saved[shot.OutputAssetID] {
			t.Fatalf("missing archived output: %+v", shot)
		}
	}
}

func TestAgentGenerationCloudArchiveStateIsIndependent(t *testing.T) {
	output := uuid.NewString()
	p := Project{Agent: &AgentGenerationRequest{}, Status: "completed", Shots: []Shot{{Status: "succeeded", OutputAssetID: output}}}
	if got := AgentGenerationCloudArchive(p); got != "pending" {
		t.Fatalf("missing receipt = %s", got)
	}
	p.TeamAssetArchives = []CanvasTeamAssetArchive{{SourceAssetID: output, Status: "failed"}}
	if got := AgentGenerationCloudArchive(p); got != "failed" || AgentGenerationStatus(p.Status) != "completed" {
		t.Fatalf("failed archive changed generation: archive=%s generation=%s", got, AgentGenerationStatus(p.Status))
	}
	p.TeamAssetArchives[0] = CanvasTeamAssetArchive{SourceAssetID: output, TeamAssetID: uuid.NewString(), Status: "saved"}
	if got := AgentGenerationCloudArchive(p); got != "saved" || AgentGenerationStage(p) != "completed" {
		t.Fatalf("saved archive state: archive=%s stage=%s", got, AgentGenerationStage(p))
	}
	if raw, err := json.Marshal(p); err != nil || len(raw) == 0 {
		t.Fatal(err)
	}
}
