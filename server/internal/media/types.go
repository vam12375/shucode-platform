package media

import "time"

const ConfigKey = "media:catalog:v1"
const IndexKey = "media:projects:v1"

type Model struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	ProviderID    string   `json:"providerId"`
	UpstreamModel string   `json:"upstreamModel"`
	Adapter       string   `json:"adapter"`
	Resolutions   []string `json:"resolutions"`
	Durations     []int    `json:"durations"`
	CostMicro     int64    `json:"costMicro"`
	GenerateAudio bool     `json:"generateAudio"`
	PriceMicro    int64    `json:"priceMicro"`
	PriceVersion  string   `json:"priceVersion"`
}
type Asset struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Product         string    `json:"product"`
	Kind            string    `json:"kind"`
	Mime            string    `json:"mime"`
	Size            int64     `json:"size"`
	CreatedAt       time.Time `json:"createdAt"`
	URL             string    `json:"url,omitempty"`
	DurationSeconds float64   `json:"durationSeconds,omitempty"`
	Width           int       `json:"width,omitempty"`
	Height          int       `json:"height,omitempty"`
	SourceURL       string    `json:"sourceUrl,omitempty"`
	Checksum        string    `json:"checksum,omitempty"`
}
type Brief struct {
	Mode                     string   `json:"mode"`
	Product                  string   `json:"product"`
	SellingPoints            string   `json:"sellingPoints"`
	AssetIDs                 []string `json:"assetIds"`
	ReferenceURLs            []string `json:"referenceUrls"`
	Platforms                []string `json:"platforms"`
	VideoModel               string   `json:"videoModel"`
	ImageModel               string   `json:"imageModel,omitempty"`
	Resolution               string   `json:"resolution"`
	AspectRatio              string   `json:"aspectRatio"`
	Duration                 int      `json:"duration"`
	MaxCreditsMicro          int64    `json:"maxCreditsMicro"`
	ReviewPlan               bool     `json:"reviewPlan"`
	ReferenceAssetID         string   `json:"referenceAssetId,omitempty"`
	ReferenceDurationSeconds float64  `json:"referenceDurationSeconds,omitempty"`
	ReplacementInstructions  string   `json:"replacementInstructions,omitempty"`
}
type Reference struct {
	URL             string    `json:"url"`
	Title           string    `json:"title"`
	CapturedAt      time.Time `json:"capturedAt"`
	Likes           *float64  `json:"likes"`
	Evidence        string    `json:"evidence"`
	SelectionReason string    `json:"selectionReason"`
}
type ShotPlan struct {
	SourceURL             string  `json:"sourceUrl"`
	SourceStart           float64 `json:"sourceStart"`
	SourceEnd             float64 `json:"sourceEnd"`
	Duration              int     `json:"duration"`
	Framing               string  `json:"framing"`
	Camera                string  `json:"camera"`
	Action                string  `json:"action"`
	Purpose               string  `json:"purpose"`
	Prompt                string  `json:"prompt"`
	ImagePrompt           string  `json:"imagePrompt,omitempty"`
	AssetID               string  `json:"assetId"`
	Subtitle              string  `json:"subtitle"`
	Transition            string  `json:"transition,omitempty"`
	TransitionDescription string  `json:"transitionDescription,omitempty"`
	Subject               string  `json:"subject,omitempty"`
	Scene                 string  `json:"scene,omitempty"`
	Sound                 string  `json:"sound,omitempty"`
}
type Plan struct {
	References []Reference `json:"references"`
	Shots      []ShotPlan  `json:"shots"`
}
type Analysis struct {
	TaskID            string `json:"taskId"`
	InputID           string `json:"inputId"`
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
}
type Shot struct {
	Plan               ShotPlan `json:"plan"`
	Status             string   `json:"status"`
	Attempt            int      `json:"attempt"`
	VideoTaskID        string   `json:"videoTaskId,omitempty"`
	ResultURL          string   `json:"resultUrl,omitempty"`
	ImageAssetID       string   `json:"imageAssetId,omitempty"`
	OutputAssetID      string   `json:"outputAssetId,omitempty"`
	Error              string   `json:"error,omitempty"`
	ChargedMicro       int64    `json:"chargedMicro"`
	QuoteMicro         int64    `json:"quoteMicro"`
	ReservationID      string   `json:"reservationId,omitempty"`
	Stage              string   `json:"stage,omitempty"`
	PendingChargeMicro int64    `json:"pendingChargeMicro,omitempty"`
	ReservedMicro      int64    `json:"reservedMicro,omitempty"`
	ReleasedMicro      int64    `json:"releasedMicro,omitempty"`
}
type Project struct {
	AnalysisFee          *FixedFee      `json:"analysisFee,omitempty"`
	AnalysisChargedMicro int64          `json:"analysisChargedMicro"`
	Canvas               *CanvasRequest `json:"canvas,omitempty"`
	ID                   string         `json:"id"`
	Version              int64          `json:"version"`
	Status               string         `json:"status"`
	Brief                Brief          `json:"brief"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
	Analysis             *Analysis      `json:"analysis,omitempty"`
	Plan                 *Plan          `json:"plan,omitempty"`
	Shots                []Shot         `json:"shots"`
	QuoteMicro           int64          `json:"quoteMicro"`
	ChargedMicro         int64          `json:"chargedMicro"`
	OutputAssetID        string         `json:"outputAssetId,omitempty"`
	Error                string         `json:"error,omitempty"`
	CancelRequested      bool           `json:"cancelRequested"`
	TeamID               string         `json:"teamId"`
	UserID               string         `json:"userId"`
	DeviceID             string         `json:"deviceId"`
	Models               []Model        `json:"models"`
	LeaseOwner           string         `json:"leaseOwner,omitempty"`
	LeaseUntil           time.Time      `json:"leaseUntil"`
}

// 用户投影不携带供应商路由、请求身份、锁定售价或 worker lease；这些字段只由平台状态机写入。
type ProjectView struct {
	AnalysisChargedMicro int64          `json:"analysisChargedMicro"`
	Canvas               *CanvasRequest `json:"canvas,omitempty"`
	ReservedMicro        int64          `json:"reservedMicro"`
	ReleasedMicro        int64          `json:"releasedMicro"`
	ID                   string         `json:"id"`
	Version              int64          `json:"version"`
	Status               string         `json:"status"`
	Brief                Brief          `json:"brief"`
	CreatedAt            time.Time      `json:"createdAt"`
	UpdatedAt            time.Time      `json:"updatedAt"`
	Analysis             *Analysis      `json:"analysis,omitempty"`
	Plan                 *Plan          `json:"plan,omitempty"`
	Shots                []ShotView     `json:"shots"`
	QuoteMicro           int64          `json:"quoteMicro"`
	ChargedMicro         int64          `json:"chargedMicro"`
	OutputAssetID        string         `json:"outputAssetId,omitempty"`
	Error                string         `json:"error,omitempty"`
	CancelRequested      bool           `json:"cancelRequested"`
}
type ShotView struct {
	Plan          ShotPlan `json:"plan"`
	Status        string   `json:"status"`
	Attempt       int      `json:"attempt"`
	ImageAssetID  string   `json:"imageAssetId,omitempty"`
	OutputAssetID string   `json:"outputAssetId,omitempty"`
	Error         string   `json:"error,omitempty"`
	ChargedMicro  int64    `json:"chargedMicro"`
	QuoteMicro    int64    `json:"quoteMicro"`
}

func (p Project) View() ProjectView {
	shots := make([]ShotView, 0, len(p.Shots))
	for _, s := range p.Shots {
		shots = append(shots, ShotView{Plan: s.Plan, Status: s.Status, Attempt: s.Attempt, ImageAssetID: s.ImageAssetID, OutputAssetID: s.OutputAssetID, Error: s.Error, ChargedMicro: s.ChargedMicro, QuoteMicro: s.QuoteMicro})
	}
	return ProjectView{AnalysisChargedMicro: p.AnalysisChargedMicro, Canvas: p.Canvas, ReservedMicro: 0, ReleasedMicro: 0, ID: p.ID, Version: p.Version, Status: p.Status, Brief: p.Brief, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Analysis: p.Analysis, Plan: p.Plan, Shots: shots, QuoteMicro: p.QuoteMicro, ChargedMicro: p.ChargedMicro, OutputAssetID: p.OutputAssetID, Error: p.Error, CancelRequested: p.CancelRequested}
}

// 阶段售价独立于余额占用；旧任务仍从持久化预留金额恢复，不能因取消预留而免费交付。
func (s Shot) pendingCharge() int64 {
	if s.PendingChargeMicro > 0 {
		return s.PendingChargeMicro
	}
	return s.ReservedMicro
}

func (s *Shot) clearPending() {
	s.ReservationID = ""
	s.PendingChargeMicro = 0
	s.ReservedMicro = 0
	s.ReleasedMicro = 0
	s.Stage = ""
}
