package apiapp

import "github.com/vam12375/shucode-platform/server/internal/media"

type consoleMediaModel struct {
	media.Model
	// 只读派生能力；写入仍使用严格 Model，不能由管理员任意开放未知上游能力。
	AspectRatios []string `json:"aspectRatios"`
}

func consoleMediaModels(models []media.Model) []consoleMediaModel {
	out := make([]consoleMediaModel, 0, len(models))
	for _, model := range models {
		model = media.NormalizeSeedanceDuration(model)
		out = append(out, consoleMediaModel{Model: model, AspectRatios: media.ModelAspectRatios(model)})
	}
	return out
}
