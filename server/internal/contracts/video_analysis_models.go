package contracts

// VideoAnalysisModel 镜像客户端 packages/shared/src/videoAnalysisModels.ts 的目录项。
type VideoAnalysisModel struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Available         bool   `json:"available"`
	UnavailableReason string `json:"unavailableReason,omitempty"`
}

type VideoAnalysisModelCatalog struct {
	Models         []VideoAnalysisModel `json:"models"`
	DefaultModelID string               `json:"defaultModelId,omitempty"`
}
