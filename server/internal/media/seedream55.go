package media

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/google/uuid"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"slices"
	"strconv"
	"strings"
)

func seedream55Model(m Model) bool {
	return m.Kind == "image" && (m.Adapter == "ark" || m.Adapter == "") && datedModelFamily(m.UpstreamModel, []string{"doubao-seedream-5-5-"})
}

func seedreamImageMime(mime string) bool {
	return slices.Contains([]string{"image/png", "image/jpeg", "image/webp", "image/bmp", "image/tiff", "image/gif", "image/heic", "image/heif"}, mime)
}

func seedreamReferenceMime(head []byte, mime string) string {
	if len(head) >= 4 && (string(head[:4]) == "II\x2a\x00" || string(head[:4]) == "MM\x00\x2a") {
		return "image/tiff"
	}
	if len(head) >= 12 && string(head[4:8]) == "ftyp" {
		if slices.Contains([]string{"heic", "heix", "hevc", "hevx"}, string(head[8:12])) {
			return "image/heic"
		}
		if slices.Contains([]string{"mif1", "msf1"}, string(head[8:12])) {
			return "image/heif"
		}
	}
	return mime
}

func (s *Service) validateSeedream55Asset(ctx context.Context, asset Asset, team string, layers bool) error {
	if !seedreamImageMime(asset.Mime) || layers && asset.Mime != "image/png" && asset.Mime != "image/jpeg" || asset.Size > 30<<20 {
		return ErrInvalid
	}
	w, h := asset.Width, asset.Height
	if w <= 0 || h <= 0 {
		file, err := os.Open(s.Blobs.Path(team, asset.ID))
		if err != nil {
			return ErrInvalid
		}
		config, _, err := image.DecodeConfig(file)
		_ = file.Close()
		if err == nil {
			w, h = config.Width, config.Height
		} else {
			metadata, err := ProbeReference(ctx, s.Blobs.Path(team, asset.ID))
			if err != nil {
				return ErrInvalid
			}
			w, h = metadata.Width, metadata.Height
		}
	}
	minPixels := 196
	if layers {
		minPixels = 262_144
	}
	if w <= 14 || h <= 14 || int64(w)*int64(h) < int64(minPixels) || int64(w)*int64(h) > 36_000_000 || float64(w)/float64(h) < 1.0/16 || float64(w)/float64(h) > 16 {
		return ErrInvalid
	}
	return nil
}

type ImageOptions struct {
	Size               string `json:"size,omitempty"`
	Background         string `json:"background,omitempty"`
	OutputFormat       string `json:"outputFormat,omitempty"`
	LayerDecomposition bool   `json:"layerDecomposition,omitempty"`
	MaxImages          int    `json:"maxImages,omitempty"`
	TextRecognition    bool   `json:"textRecognition,omitempty"`
	Watermark          *bool  `json:"watermark,omitempty"`
	OptimizePromptMode string `json:"optimizePromptMode,omitempty"`
	SafetyIdentifier   string `json:"safetyIdentifier,omitempty"`
}

type ImageCapabilities struct {
	MaxImages       int      `json:"maxImages"`
	MaxLayerOutputs int      `json:"maxLayerOutputs"`
	Backgrounds     []string `json:"backgrounds"`
	OutputFormats   []string `json:"outputFormats"`
	CustomSize      bool     `json:"customSize"`
	TextRecognition bool     `json:"textRecognition"`
}

func ImageCapabilitiesFor(m Model) *ImageCapabilities {
	if !seedream55Model(m) {
		return nil
	}
	return &ImageCapabilities{MaxImages: 10, MaxLayerOutputs: 100, Backgrounds: []string{"auto", "opaque", "transparent"}, OutputFormats: []string{"png", "jpeg"}, CustomSize: true, TextRecognition: true}
}

type ImagePriceTier struct {
	MaxPixels  int64 `json:"maxPixels"`
	PriceMicro int64 `json:"priceMicro"`
	CostMicro  int64 `json:"costMicro"`
}

type ImagePricing struct {
	InputPriceMicro int64            `json:"inputPriceMicro"`
	InputCostMicro  int64            `json:"inputCostMicro"`
	OutputTiers     []ImagePriceTier `json:"outputTiers"`
}

func validateImagePricing(m Model) error {
	if !seedream55Model(m) {
		if m.ImagePricing != nil {
			return ErrInvalid
		}
		return nil
	}
	p := m.ImagePricing
	if p == nil || p.InputPriceMicro <= 0 || p.InputPriceMicro > 100_000_000 || p.InputCostMicro < 0 || p.InputCostMicro > 100_000_000 || len(p.OutputTiers) != 2 {
		return ErrInvalid
	}
	for i, tier := range p.OutputTiers {
		if tier.MaxPixels != []int64{2_610_000, 4_630_000}[i] || tier.PriceMicro <= 0 || tier.PriceMicro > 100_000_000 || tier.CostMicro < 0 || tier.CostMicro > 100_000_000 {
			return ErrInvalid
		}
	}
	return nil
}

func imagePixelSize(size string) (int, int, bool) {
	parts := strings.Split(size, "x")
	if len(parts) != 2 {
		return 0, 0, false
	}
	w, e1 := strconv.Atoi(parts[0])
	h, e2 := strconv.Atoi(parts[1])
	return w, h, e1 == nil && e2 == nil && w > 0 && h > 0 && w <= 100_000 && h <= 100_000 && fmt.Sprintf("%dx%d", w, h) == size
}

func validateSeedream55Input(m Model, r CanvasRequest, images int) error {
	if !seedream55Model(m) {
		if r.ImageOptions != nil {
			return ErrInvalid
		}
		return nil
	}
	if images > 10 {
		return ErrInvalid
	}
	o := r.ImageOptions
	if o == nil {
		return nil
	}
	if o.OptimizePromptMode != "" && o.OptimizePromptMode != "standard" || len(o.SafetyIdentifier) > 64 {
		return ErrInvalid
	}
	if o.OutputFormat != "" && !slices.Contains([]string{"png", "jpeg"}, o.OutputFormat) {
		return ErrInvalid
	}
	if o.LayerDecomposition {
		if images != 1 || r.Count > 1 || o.Background != "" || o.MaxImages != 0 && (o.MaxImages < 2 || o.MaxImages > 100) || o.Size != "" && o.Size != "auto" {
			return ErrInvalid
		}
	} else {
		if o.MaxImages != 0 || o.TextRecognition || o.Background != "" && !slices.Contains([]string{"auto", "opaque", "transparent"}, o.Background) || o.Background == "transparent" && o.OutputFormat == "jpeg" {
			return ErrInvalid
		}
		if o.Size != "" {
			w, h, ok := imagePixelSize(o.Size)
			pixels := int64(w) * int64(h)
			if !ok || pixels < 921_600 || pixels > 4_624_220 || float64(w)/float64(h) < 1.0/16 || float64(w)/float64(h) > 16 {
				return ErrInvalid
			}
		}
	}
	return nil
}

func seedream55Quote(m Model, r CanvasRequest, inputs int) int64 {
	if m.ImagePricing == nil {
		return 0
	}
	maxOutputs, pixels := 1, int64(4_630_000)
	if o := r.ImageOptions; o != nil && o.LayerDecomposition {
		maxOutputs = o.MaxImages
		if maxOutputs == 0 {
			maxOutputs = 30
		}
	} else if o != nil && o.Size != "" {
		w, h, ok := imagePixelSize(o.Size)
		if !ok {
			return 0
		}
		pixels = int64(w) * int64(h)
	}
	// 模型档位由上游决定实际尺寸，报价用最高像素档；实扣只按响应逐张尺寸。
	return int64(inputs)*m.ImagePricing.InputPriceMicro + int64(maxOutputs)*imagePixelRate(m.ImagePricing, pixels, false)
}

func imagePixelRate(p *ImagePricing, pixels int64, cost bool) int64 {
	if p == nil || pixels <= 0 {
		return 0
	}
	for _, tier := range p.OutputTiers {
		if pixels <= tier.MaxPixels {
			if cost {
				return tier.CostMicro
			}
			return tier.PriceMicro
		}
	}
	return 0
}

type ImageBoundingBox struct {
	Absolute   [4]int `json:"absolute"`
	Normalized [4]int `json:"normalized"`
}
type ImageTextStyle struct {
	SimilarFontFamily string `json:"similarFontFamily"`
	ForegroundColor   string `json:"foregroundColor"`
	Bold              bool   `json:"bold"`
	Italic            bool   `json:"italic"`
	Underline         bool   `json:"underline"`
	Strikethrough     bool   `json:"strikethrough"`
	Shadow            bool   `json:"shadow"`
	Translucent       bool   `json:"translucent"`
}
type ImageTextRun struct {
	Text  string         `json:"text"`
	Style ImageTextStyle `json:"style"`
}
type ImageTextLine struct {
	BoundingBox ImageBoundingBox `json:"boundingBox"`
	Runs        []ImageTextRun   `json:"runs"`
}
type ImageTextRecognition struct {
	Text  string          `json:"text"`
	Lines []ImageTextLine `json:"lines"`
}
type ImageOutput struct {
	AssetID         string                `json:"assetId,omitempty"`
	ResultURL       string                `json:"resultUrl,omitempty"`
	Width           int                   `json:"width"`
	Height          int                   `json:"height"`
	OutputFormat    string                `json:"outputFormat,omitempty"`
	Background      string                `json:"background,omitempty"`
	ZIndex          *int                  `json:"zIndex,omitempty"`
	Name            string                `json:"name,omitempty"`
	Description     string                `json:"description,omitempty"`
	BoundingBox     *ImageBoundingBox     `json:"boundingBox,omitempty"`
	TextRecognition *ImageTextRecognition `json:"textRecognition,omitempty"`
}

// 官方 OCR wire 使用 snake_case，平台协议统一 camelCase；只翻译已知字段，不透传任意上游对象。
func decodeImageTextRecognition(raw json.RawMessage) *ImageTextRecognition {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	names := map[string]string{"bounding_box": "boundingBox", "similar_font_family": "similarFontFamily", "foreground_color": "foregroundColor"}
	var rename func(any) any
	rename = func(v any) any {
		switch x := v.(type) {
		case map[string]any:
			n := map[string]any{}
			for k, v := range x {
				key := k
				if alias, ok := names[k]; ok {
					key = alias
				}
				n[key] = rename(v)
			}
			return n
		case []any:
			for i := range x {
				x[i] = rename(x[i])
			}
			return x
		default:
			return v
		}
	}
	encoded, _ := json.Marshal(rename(value))
	var out ImageTextRecognition
	if json.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return &out
}

func (p Provider) submitSeedream55(ctx context.Context, m Model, r CanvasRequest, images []string) (ProviderImageSubmission, error) {
	if err := validateSeedream55Input(m, r, len(images)); err != nil {
		return ProviderImageSubmission{}, err
	}
	size, prompt := canvasImageParameters(m, r)
	body := map[string]any{"model": m.UpstreamModel, "size": size, "response_format": "url", "watermark": false, "output_format": "png"}
	if prompt != "" {
		body["prompt"] = prompt
	}
	if len(images) == 1 {
		body["image"] = images[0]
	} else if len(images) > 1 {
		body["image"] = images
	}
	if o := r.ImageOptions; o != nil {
		if o.Size != "" {
			body["size"] = o.Size
		}
		if o.OutputFormat != "" {
			body["output_format"] = o.OutputFormat
		}
		if o.Background != "" {
			body["background"] = o.Background
		}
		if o.Watermark != nil {
			body["watermark"] = *o.Watermark
		}
		if o.OptimizePromptMode != "" {
			body["optimize_prompt_options"] = map[string]string{"mode": o.OptimizePromptMode}
		}
		if o.SafetyIdentifier != "" {
			body["safety_identifier"] = o.SafetyIdentifier
		}
		if o.LayerDecomposition {
			body["layer_decomposition"] = true
			maxImages := o.MaxImages
			if maxImages == 0 {
				maxImages = 30
			}
			body["layer_decomposition_options"] = map[string]any{"max_images": maxImages, "text_recognition": o.TextRecognition}
			if o.Size == "" {
				body["size"] = r.Resolution
			}
			delete(body, "prompt")
			if r.Prompt != "" {
				body["prompt"] = r.Prompt
			}
		}
	}
	var out struct {
		Data []struct {
			URL             string            `json:"url"`
			Size            string            `json:"size"`
			OutputFormat    string            `json:"output_format"`
			Background      string            `json:"background"`
			ZIndex          *int              `json:"z_index"`
			Name            string            `json:"name"`
			Description     string            `json:"description"`
			BoundingBox     *ImageBoundingBox `json:"bounding_box"`
			TextRecognition json.RawMessage   `json:"text_recognition"`
		} `json:"data"`
		Usage struct {
			InputImages     *int `json:"input_images"`
			GeneratedImages *int `json:"generated_images"`
		} `json:"usage"`
	}
	if err := p.request(ctx, "POST", "/images/generations", body, &out); err != nil {
		return ProviderImageSubmission{}, err
	}
	result := ProviderImageSubmission{InputImages: out.Usage.InputImages}
	for _, item := range out.Data {
		w, h, valid := imagePixelSize(item.Size)
		if !ValidURL(item.URL) || !valid {
			return ProviderImageSubmission{}, fmt.Errorf("provider_image_billing_missing")
		}
		result.Outputs = append(result.Outputs, ImageOutput{ResultURL: item.URL, Width: w, Height: h, OutputFormat: item.OutputFormat, Background: item.Background, ZIndex: item.ZIndex, Name: item.Name, Description: item.Description, BoundingBox: item.BoundingBox, TextRecognition: decodeImageTextRecognition(item.TextRecognition)})
	}
	limit := 1
	if o := r.ImageOptions; o != nil && o.LayerDecomposition {
		limit = o.MaxImages
		if limit == 0 {
			limit = 30
		}
	}
	if len(result.Outputs) == 0 || len(result.Outputs) > limit || out.Usage.InputImages == nil || *out.Usage.InputImages != len(images) || out.Usage.GeneratedImages == nil || *out.Usage.GeneratedImages != len(result.Outputs) {
		return ProviderImageSubmission{}, fmt.Errorf("provider_image_billing_missing")
	}
	if limit == 1 && len(result.Outputs) != 1 {
		return ProviderImageSubmission{}, fmt.Errorf("provider_image_billing_missing")
	}
	result.URL = result.Outputs[0].ResultURL
	charge, _ := seedream55Charges(m, result)
	if charge <= 0 {
		return ProviderImageSubmission{}, fmt.Errorf("provider_image_billing_missing")
	}
	return result, nil
}

func seedream55Charges(m Model, result ProviderImageSubmission) (int64, int64) {
	if m.ImagePricing == nil || result.InputImages == nil {
		return 0, 0
	}
	price := int64(*result.InputImages) * m.ImagePricing.InputPriceMicro
	cost := int64(*result.InputImages) * m.ImagePricing.InputCostMicro
	for _, output := range result.Outputs {
		pixels := int64(output.Width) * int64(output.Height)
		rate := imagePixelRate(m.ImagePricing, pixels, false)
		if rate <= 0 {
			return 0, 0
		}
		price += rate
		cost += imagePixelRate(m.ImagePricing, pixels, true)
	}
	return price, cost
}

func (s *Service) saveSeedream55Outputs(ctx context.Context, p *Project, index int, shot Shot) error {
	for i, output := range shot.ImageOutputs {
		if output.AssetID != "" {
			continue
		}
		// 每层有稳定落盘身份，租约切换或保存回执失败不会重生成、重下载或重扣同一层。
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(shot.ReservationID+":"+strconv.Itoa(i))).String()
		a, err := s.Blobs.DownloadWithID(ctx, p.TeamID, id, output.ResultURL, "image")
		if err != nil {
			_, e := s.workerUpdate(ctx, p, func(v *Project) { v.Shots[index].Error = "result_download_failed" })
			return e
		}
		file, err := os.Open(s.Blobs.Path(p.TeamID, a.ID))
		if err != nil {
			return err
		}
		config, _, err := image.DecodeConfig(file)
		_ = file.Close()
		if err != nil || config.Width != output.Width || config.Height != output.Height {
			_, e := s.workerUpdate(ctx, p, func(v *Project) {
				v.Status, v.Error = "reconciliation_required", "provider_image_size_mismatch"
				v.Shots[index].Status, v.Shots[index].Error = "reconciliation_required", v.Error
			})
			return e
		}
		a.Width, a.Height = config.Width, config.Height
		if err := s.Repo.AddAsset(ctx, p.TeamID, a); err != nil {
			return err
		}
		_, err = s.workerUpdate(ctx, p, func(v *Project) { v.Shots[index].ImageOutputs[i].AssetID = a.ID; v.Shots[index].Error = "" })
		return err
	}
	if len(shot.ImageOutputs) == 0 || shot.ImageInputCount == nil || shot.ImageChargeMicro <= 0 {
		return s.unknown(ctx, p, index)
	}
	_, err := s.workerUpdate(ctx, p, func(v *Project) {
		v.Shots[index].ImageAssetID = shot.ImageOutputs[0].AssetID
		v.Shots[index].Status = "image_saved"
	})
	return err
}
