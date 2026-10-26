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
	Durations      []int                     `json:"durations,omitempty"`
	MinDuration    int                       `json:"minDuration,omitempty"`
	MaxDuration    int                       `json:"maxDuration,omitempty"`
	CostMicro      int64                     `json:"costMicro"`
	GenerateAudio  bool                      `json:"generateAudio"`
	PriceMicro     int64                     `json:"priceMicro"`
	PriceVersion   string                    `json:"priceVersion"`
	VideoReference *VideoReferenceCapability `json:"videoReference,omitempty"`
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

type VideoReferenceCapability struct {
	MaxDurationSeconds int `json:"maxDurationSeconds"`
	MaxImages          int `json:"maxImages"`
	// MaxAudioSeconds 是参考音频单段上限；0 表示该型号不接受参考音频。
	// 整段复刻的音频与原片同源同区间，永远只有一段，因此没有段数字段。
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
	Audience          string `json:"audience,omitempty"`
	Tone              string `json:"tone,omitempty"`
	HookStyle         string `json:"hookStyle,omitempty"`
	CallToAction      string `json:"callToAction,omitempty"`
	AvoidClaims       string `json:"avoidClaims,omitempty"`
	Script            string `json:"script,omitempty"`
	NarrationLanguage string `json:"narrationLanguage,omitempty"`
	VariantCount      int    `json:"variantCount,omitempty"`
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
type Project struct {
	SourceChecksum       string                   `json:"sourceChecksum,omitempty"`
	SourceAnalysis       *SourceAnalysis          `json:"sourceAnalysis,omitempty"`
	AnalysisFee          *FixedFee                `json:"analysisFee,omitempty"`
	AnalysisChargedMicro int64                    `json:"analysisChargedMicro"`
	Canvas               *CanvasRequest           `json:"canvas,omitempty"`
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
	ChargedMicro         int64                    `json:"chargedMicro"`
	OutputAssetID        string                   `json:"outputAssetId,omitempty"`
	Error                string                   `json:"error,omitempty"`
	CancelRequested      bool                     `json:"cancelRequested"`
	TeamID               string                   `json:"teamId"`
	UserID               string                   `json:"userId"`
	DeviceID             string                   `json:"deviceId"`
	Models               []Model                  `json:"models"`
	LeaseOwner           string                   `json:"leaseOwner,omitempty"`
	LeaseUntil           time.Time                `json:"leaseUntil"`
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
	Plan          ShotPlan       `json:"plan"`
	Status        string         `json:"status"`
	Attempt       int            `json:"attempt"`
	VideoTaskID   string         `json:"videoTaskId,omitempty"`
	ImageAssetID  string         `json:"imageAssetId,omitempty"`
	OutputAssetID string         `json:"outputAssetId,omitempty"`
	Error         string         `json:"error,omitempty"`
	ProviderError *ProviderError `json:"providerError,omitempty"`
	ChargedMicro  int64          `json:"chargedMicro"`
	QuoteMicro    int64          `json:"quoteMicro"`
}

func (p Project) View() ProjectView {
	shots := make([]ShotView, 0, len(p.Shots))
	for _, s := range p.Shots {
		var diagnostic *ProviderError
		if s.ProviderError != nil {
			diagnostic = sanitizeProviderError(s.ProviderError.Code)
		}
		shots = append(shots, ShotView{Plan: s.Plan, Status: s.Status, Attempt: s.Attempt, VideoTaskID: publicVideoTaskID(s.VideoTaskID), ImageAssetID: s.ImageAssetID, OutputAssetID: s.OutputAssetID, Error: s.Error, ProviderError: diagnostic, ChargedMicro: s.ChargedMicro, QuoteMicro: s.QuoteMicro})
	}
	var archives []CanvasTeamAssetArchive
	if p.Canvas != nil {
		archives = canvasCurrentArchives(p)
	}
	return ProjectView{SourceAnalysis: cloneSourceAnalysis(p.SourceAnalysis), AnalysisChargedMicro: p.AnalysisChargedMicro, Canvas: p.Canvas, TeamAssetArchives: archives, ReservedMicro: 0, ReleasedMicro: 0, ID: p.ID, Version: p.Version, Status: p.Status, Brief: p.Brief, CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt, Analysis: p.Analysis, Plan: p.Plan, Shots: shots, QuoteMicro: p.QuoteMicro, ChargedMicro: p.ChargedMicro, OutputAssetID: p.OutputAssetID, Error: p.Error, CancelRequested: p.CancelRequested}
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
