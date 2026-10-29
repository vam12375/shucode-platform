package media

import (
	"slices"
	"time"
)

const ConfigKey = "media:catalog:v1"
const IndexKey = "media:projects:v1"

type Model struct {
	ID            string       `json:"id"`
	Name          string       `json:"name"`
	DisplayName   string       `json:"displayName,omitempty"`
	Kind          string       `json:"kind"`
	ProviderID    string       `json:"providerId"`
	UpstreamModel string       `json:"upstreamModel"`
	Adapter       string       `json:"adapter"`
	Resolutions   []string     `json:"resolutions"`
	Routes        []ModelRoute `json:"routes,omitempty"`
	// Durations 是旧版枚举时长；MinDuration/MaxDuration 是连续区间（秒）。
	// 区间一旦配置即优先，Durations 仅作缺省时的兼容回退。
	Durations     []int `json:"durations,omitempty"`
	MinDuration   int   `json:"minDuration,omitempty"`
	MaxDuration   int   `json:"maxDuration,omitempty"`
	CostMicro     int64 `json:"costMicro"`
	GenerateAudio bool  `json:"generateAudio"`
	PriceMicro    int64 `json:"priceMicro"`
	// BillingUnit 为空时保持历史视频按秒、图片按张的口径；request/token 用于 ToAPIs 新计费模型。
	BillingUnit               string                    `json:"billingUnit,omitempty"`
	TokenPriceMicroPerMillion int64                     `json:"tokenPriceMicroPerMillion,omitempty"`
	TokenCostMicroPerMillion  int64                     `json:"tokenCostMicroPerMillion,omitempty"`
	PriceVersion              string                    `json:"priceVersion"`
	VideoReference            *VideoReferenceCapability `json:"videoReference,omitempty"`
}

const (
	BillingUnitSecond  = "second"
	BillingUnitRequest = "request"
	BillingUnitToken   = "token"
)

// EffectiveBillingUnit 将旧目录映射到原有口径，避免历史快照改变结算语义。
func (m Model) EffectiveBillingUnit() string {
	if m.BillingUnit != "" {
		return m.BillingUnit
	}
	if m.Kind == "image" {
		return "image"
	}
	return BillingUnitSecond
}

// 媒体路由共用目录中的真实型号与能力，避免切换供应商后改变已报价的输入语义。
type ModelRoute struct {
	ProviderID string `json:"providerId"`
	Priority   int64  `json:"priority"`
	Weight     int64  `json:"weight"`
	Enabled    bool   `json:"enabled"`
}

// DurationSupported 判断单个镜头秒数是否可用：区间优先，旧枚举兜底。
func (m Model) DurationSupported(d int) bool {
	if m.MinDuration > 0 && m.MaxDuration >= m.MinDuration {
		return d >= m.MinDuration && d <= m.MaxDuration
	}
	return slices.Contains(m.Durations, d)
}

// DurationRange 返回对外暴露的镜头时长上下界；旧枚举按最小/最大推导。
func (m Model) DurationRange() (int, int, bool) {
	if m.MinDuration > 0 && m.MaxDuration >= m.MinDuration {
		return m.MinDuration, m.MaxDuration, true
	}
	if len(m.Durations) == 0 {
		return 0, 0, false
	}
	minDuration, maxDuration := m.Durations[0], m.Durations[0]
	for _, d := range m.Durations[1:] {
		if d < minDuration {
			minDuration = d
		}
		if d > maxDuration {
			maxDuration = d
		}
	}
	return minDuration, maxDuration, true
}

// AllowedDurations 返回可用于组合总时长的单镜头秒数；区间展开为逐秒。
func (m Model) AllowedDurations() []int {
	if m.MinDuration > 0 && m.MaxDuration >= m.MinDuration {
		out := make([]int, 0, m.MaxDuration-m.MinDuration+1)
		for d := m.MinDuration; d <= m.MaxDuration; d++ {
			out = append(out, d)
		}
		return out
	}
	return m.Durations
}

// SeedanceDurationRange returns the verified continuous duration range for
// Seedance 2.x models. Older catalog entries sometimes stored the provider's
// former 4/5/10/15-second list; callers should normalize those entries before
// exposing or executing them.
func SeedanceDurationRange(m Model) (int, int, bool) {
	adapter := m.Adapter
	if adapter == "" {
		adapter = "ark"
	}
	switch adapter {
	case "ark":
		if seedance20Model(m.UpstreamModel) {
			return 4, 15, true
		}
	case "toapis":
		if toapisSeedanceModel(m.UpstreamModel) {
			return 4, 15, true
		}
	}
	return 0, 0, false
}

// NormalizeSeedanceDuration upgrades a catalog entry to the current 4–15
// second continuous contract while leaving unrelated legacy models untouched.
func NormalizeSeedanceDuration(m Model) Model {
	if minDuration, maxDuration, ok := SeedanceDurationRange(m); ok {
		m.MinDuration, m.MaxDuration = minDuration, maxDuration
		m.Durations = nil
	}
	return m
}

type VideoReferenceCapability struct {
	MaxDurationSeconds int `json:"maxDurationSeconds"`
	MaxImages          int `json:"maxImages"`
	// MaxSegments 是独立复刻允许的平台执行段数；0 与 1 都保持旧版单整片语义。
	MaxSegments int `json:"maxSegments,omitempty"`
	// MaxAudioSeconds 是参考音频单段上限；0 表示该型号不接受参考音频。
	// 每个执行段的音频与视频同源同区间。
	MaxAudioSeconds int   `json:"maxAudioSeconds,omitempty"`
	PriceMicro      int64 `json:"priceMicro"`
	CostMicro       int64 `json:"costMicro"`
	// Token* 为「输入包含视频」的每百万视频 token 费率（micro-credit）。
	// 非零时优先按 Ark completion_tokens 结算；旧按秒价格仅作兼容回退。
	TokenPriceMicroPerMillion int64 `json:"tokenPriceMicroPerMillion,omitempty"`
	TokenCostMicroPerMillion  int64 `json:"tokenCostMicroPerMillion,omitempty"`
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
	ThumbnailURL    string    `json:"thumbnailUrl,omitempty"`
	PosterURL       string    `json:"posterUrl,omitempty"`
	DurationSeconds float64   `json:"durationSeconds,omitempty"`
	Width           int       `json:"width,omitempty"`
	Height          int       `json:"height,omitempty"`
	SourceURL       string    `json:"sourceUrl,omitempty"`
	Checksum        string    `json:"checksum,omitempty"`
	Internal        bool      `json:"internal,omitempty"`
}
type Brief struct {
	SourceOnly               bool     `json:"sourceOnly,omitempty"`
	GenerationMode           string   `json:"generationMode,omitempty"`
	ReferenceMode            string   `json:"referenceMode,omitempty"`
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
	// 创作意图与文案控制，全部可选；客户端仅在 overview 声明 capability 时发送。
	Audience          string            `json:"audience,omitempty"`
	Tone              string            `json:"tone,omitempty"`
	HookStyle         string            `json:"hookStyle,omitempty"`
	CallToAction      string            `json:"callToAction,omitempty"`
	AvoidClaims       string            `json:"avoidClaims,omitempty"`
	Script            string            `json:"script,omitempty"`
	NarrationLanguage string            `json:"narrationLanguage,omitempty"`
	VariantCount      int               `json:"variantCount,omitempty"`
	Finishing         *FinishingOptions `json:"finishing,omitempty"`
}

// FinishingOptions 只用于新自动制作；缺省保留历史片段原音与字幕语义。
type FinishingOptions struct {
	Voiceover bool   `json:"voiceover"`
	Music     bool   `json:"music"`
	Subtitles string `json:"subtitles"`
}

// NarrationState 由同一媒体 worker 持久化，未知受理不得自动重新提交。
type NarrationState struct {
	Signature       string  `json:"signature"`
	RequestID       string  `json:"requestId"`
	Text            string  `json:"text"`
	Status          string  `json:"status"`
	AssetID         string  `json:"assetId,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
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
	ReferenceVideoAssetID string  `json:"referenceVideoAssetId,omitempty"`
	// ReferenceAudioAssetID 与视频片段同源同区间，由 Host 真实上传后经 revise 绑定。
	ReferenceAudioAssetID string `json:"referenceAudioAssetId,omitempty"`
	Duration              int    `json:"duration"`
	Framing               string `json:"framing"`
	Camera                string `json:"camera"`
	Action                string `json:"action"`
	Purpose               string `json:"purpose"`
	Prompt                string `json:"prompt"`
	ImagePrompt           string `json:"imagePrompt,omitempty"`
	AssetID               string `json:"assetId"`
	Subtitle              string `json:"subtitle"`
	// 口播文本与屏幕字幕分离；voiceover 未启用时忽略。
	Narration             string `json:"narration,omitempty"`
	Transition            string `json:"transition,omitempty"`
	TransitionDescription string `json:"transitionDescription,omitempty"`
	Subject               string `json:"subject,omitempty"`
	Scene                 string `json:"scene,omitempty"`
	Sound                 string `json:"sound,omitempty"`
}
type Plan struct {
	References []Reference `json:"references"`
	Shots      []ShotPlan  `json:"shots"`
}
type Analysis struct {
	Kind              string `json:"kind,omitempty"`
	TaskID            string `json:"taskId"`
	InputID           string `json:"inputId"`
	WorkspacePath     string `json:"workspacePath"`
	WorkspaceIdentity string `json:"workspaceIdentity,omitempty"`
}
type Shot struct {
	CanvasVideoAssetIDs   []string       `json:"canvasVideoAssetIds,omitempty"`
	LastFrameAssetID      string         `json:"lastFrameAssetId,omitempty"`
	LastFrameResultURL    string         `json:"lastFrameResultUrl,omitempty"`
	Plan                  ShotPlan       `json:"plan"`
	Status                string         `json:"status"`
	Attempt               int            `json:"attempt"`
	VideoTaskID           string         `json:"videoTaskId,omitempty"`
	ImageTaskID           string         `json:"imageTaskId,omitempty"`
	ImageNextQueryAt      time.Time      `json:"imageNextQueryAt,omitempty"`
	ReferenceVideoAssetID string         `json:"referenceVideoAssetId,omitempty"`
	ReferenceAudioAssetID string         `json:"referenceAudioAssetId,omitempty"`
	ResultURL             string         `json:"resultUrl,omitempty"`
	ImageAssetID          string         `json:"imageAssetId,omitempty"`
	OutputAssetID         string         `json:"outputAssetId,omitempty"`
	Error                 string         `json:"error,omitempty"`
	ProviderError         *ProviderError `json:"providerError,omitempty"`
	ChargedMicro          int64          `json:"chargedMicro"`
	QuoteMicro            int64          `json:"quoteMicro"`
	ReservationID         string         `json:"reservationId,omitempty"`
	Stage                 string         `json:"stage,omitempty"`
	ProviderID            string         `json:"providerId,omitempty"`
	PendingChargeMicro    int64          `json:"pendingChargeMicro,omitempty"`
	ReservedMicro         int64          `json:"reservedMicro,omitempty"`
	ReleasedMicro         int64          `json:"releasedMicro,omitempty"`
	// Token 结算：成功后先落实际 token 与费用，下载/重试/结算都复用同一份事实。
	ProviderUsageTokens      int64 `json:"providerUsageTokens,omitempty"`
	TokenChargeMicro         int64 `json:"tokenChargeMicro,omitempty"`
	TokenRateMicroPerMillion int64 `json:"tokenRateMicroPerMillion,omitempty"`
	TokenCostMicroPerMillion int64 `json:"tokenCostMicroPerMillion,omitempty"`
}

// AgentGenerationRequest 是对话入口的独立不可变快照，不包含 Canvas 身份字段。
type AgentGenerationRequest struct {
	RequestID          string   `json:"requestId"`
	Intent             string   `json:"intent"`
	Kind               string   `json:"kind"`
	Prompt             string   `json:"prompt"`
	AssetIDs           []string `json:"assetIds"`
	ModelID            string   `json:"modelId"`
	Resolution         string   `json:"resolution"`
	AspectRatio        string   `json:"aspectRatio"`
	Duration           int      `json:"duration"`
	Count              int      `json:"count"`
	ExpectedQuoteMicro int64    `json:"expectedQuoteMicro"`
}

type Project struct {
	Narration            *NarrationState          `json:"narration,omitempty"`
	SourceChecksum       string                   `json:"sourceChecksum,omitempty"`
	SourceAnalysis       *SourceAnalysis          `json:"sourceAnalysis,omitempty"`
	AnalysisFee          *FixedFee                `json:"analysisFee,omitempty"`
	AnalysisChargedMicro int64                    `json:"analysisChargedMicro"`
	Canvas               *CanvasRequest           `json:"canvas,omitempty"`
	Agent                *AgentGenerationRequest  `json:"agentGeneration,omitempty"`
	TeamAssetArchives    []CanvasTeamAssetArchive `json:"teamAssetArchives,omitempty"`
	ID                   string                   `json:"id"`
	Version              int64                    `json:"version"`
	Status               string                   `json:"status"`
	Brief                Brief                    `json:"brief"`
	CreatedAt            time.Time                `json:"createdAt"`
	UpdatedAt            time.Time                `json:"updatedAt"`
	Analysis             *Analysis                `json:"analysis,omitempty"`
	Plan                 *Plan                    `json:"plan,omitempty"`
	Shots                []Shot                   `json:"shots"`
	QuoteMicro           int64                    `json:"quoteMicro"`
	// CompositionAssetID 在合成开始前持久化，跨 worker lease/进程恢复复用同一最终资产身份。
	// 这是平台内部恢复事实，不进入 ProjectView；OutputAssetID 仍是已完成成片的公开事实。
	CompositionAssetID string    `json:"compositionAssetId,omitempty"`
	ChargedMicro       int64     `json:"chargedMicro"`
	OutputAssetID      string    `json:"outputAssetId,omitempty"`
	Error              string    `json:"error,omitempty"`
	CancelRequested    bool      `json:"cancelRequested"`
	TeamID             string    `json:"teamId"`
	UserID             string    `json:"userId"`
	DeviceID           string    `json:"deviceId"`
	Models             []Model   `json:"models"`
	LeaseOwner         string    `json:"leaseOwner,omitempty"`
	LeaseUntil         time.Time `json:"leaseUntil"`
}

// 用户投影不携带供应商路由、准入身份、锁定售价或 worker lease；opaque task ID 仅用于受理证据与排查。
type ProjectView struct {
	SourceAnalysis       *SourceAnalysis          `json:"sourceAnalysis,omitempty"`
	AnalysisChargedMicro int64                    `json:"analysisChargedMicro"`
	Canvas               *CanvasRequest           `json:"canvas,omitempty"`
	TeamAssetArchives    []CanvasTeamAssetArchive `json:"teamAssetArchives,omitempty"`
	ReservedMicro        int64                    `json:"reservedMicro"`
	ReleasedMicro        int64                    `json:"releasedMicro"`
	ID                   string                   `json:"id"`
	Version              int64                    `json:"version"`
	Status               string                   `json:"status"`
	Brief                Brief                    `json:"brief"`
	CreatedAt            time.Time                `json:"createdAt"`
	UpdatedAt            time.Time                `json:"updatedAt"`
	Analysis             *Analysis                `json:"analysis,omitempty"`
	Plan                 *Plan                    `json:"plan,omitempty"`
	Shots                []ShotView               `json:"shots"`
	QuoteMicro           int64                    `json:"quoteMicro"`
	ChargedMicro         int64                    `json:"chargedMicro"`
	OutputAssetID        string                   `json:"outputAssetId,omitempty"`
	Error                string                   `json:"error,omitempty"`
	CancelRequested      bool                     `json:"cancelRequested"`
}
type ShotView struct {
	LastFrameAssetID string         `json:"lastFrameAssetId,omitempty"`
	Plan             ShotPlan       `json:"plan"`
	Status           string         `json:"status"`
	Attempt          int            `json:"attempt"`
	VideoTaskID      string         `json:"videoTaskId,omitempty"`
	ImageAssetID     string         `json:"imageAssetId,omitempty"`
	OutputAssetID    string         `json:"outputAssetId,omitempty"`
	Error            string         `json:"error,omitempty"`
	ProviderError    *ProviderError `json:"providerError,omitempty"`
	ChargedMicro     int64          `json:"chargedMicro"`
	QuoteMicro       int64          `json:"quoteMicro"`
}

func (p Project) View() ProjectView {
	shots := make([]ShotView, 0, len(p.Shots))
	for _, s := range p.Shots {
		var diagnostic *ProviderError
		if s.ProviderError != nil {
			diagnostic = sanitizeProviderError(s.ProviderError.Code)
		}
		shots = append(shots, ShotView{Plan: s.Plan, Status: s.Status, Attempt: s.Attempt, VideoTaskID: publicVideoTaskID(s.VideoTaskID), ImageAssetID: s.ImageAssetID, OutputAssetID: s.OutputAssetID, LastFrameAssetID: s.LastFrameAssetID, Error: s.Error, ProviderError: diagnostic, ChargedMicro: s.ChargedMicro, QuoteMicro: s.QuoteMicro})
	}
	archives := currentGeneratedArchives(p)
	return ProjectView{SourceAnalysis: cloneSourceAnalysis(p.SourceAnalysis), AnalysisChargedMicro: p.AnalysisChargedMicro, Canvas: p.Canvas, TeamAssetArchives: archives, ReservedMicro: 0, ReleasedMicro: 0, ID: p.ID, Version: p.Version, Status: p.Status, Brief: p.Brief, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Analysis: p.Analysis, Plan: p.Plan, Shots: shots, QuoteMicro: p.QuoteMicro, ChargedMicro: p.ChargedMicro, OutputAssetID: p.OutputAssetID, Error: p.Error, CancelRequested: p.CancelRequested}
}

func directGenerationRequest(p Project) (CanvasRequest, bool) {
	if p.Canvas != nil {
		return *p.Canvas, true
	}
	if p.Agent != nil {
		r := p.Agent
		return CanvasRequest{Kind: r.Kind, ModelID: r.ModelID, Prompt: r.Prompt, AssetIDs: slices.Clone(r.AssetIDs), Resolution: r.Resolution, AspectRatio: r.AspectRatio, Duration: r.Duration, Count: r.Count, ExpectedQuoteMicro: r.ExpectedQuoteMicro}, true
	}
	return CanvasRequest{}, false
}

func isDirectGeneration(p Project) bool {
	return p.Canvas != nil || p.Agent != nil
}

// 阶段售价独立于余额占用；旧任务仍从持久化预留金额恢复，不能因取消预留而免费交付。
func (s Shot) pendingCharge() int64 {
	if s.PendingChargeMicro > 0 {
		return s.PendingChargeMicro
	}
	return s.ReservedMicro
}

func (s *Shot) clearPending() {
	s.ImageNextQueryAt = time.Time{}
	s.ReservationID = ""
	s.PendingChargeMicro = 0
	s.ReservedMicro = 0
	s.ReleasedMicro = 0
	s.Stage = ""
	s.ProviderID = ""
	s.ProviderUsageTokens = 0
	s.TokenChargeMicro = 0
	s.TokenRateMicroPerMillion = 0
	s.TokenCostMicroPerMillion = 0
}
