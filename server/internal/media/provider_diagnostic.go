package media

import (
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// ProviderDiagnostic 是服务端排查历史；公开投影不得携带上游原文和内部路由。
type ProviderDiagnostic struct {
	At            time.Time `json:"at"`
	Shot          int       `json:"shot"`
	Attempt       int       `json:"attempt"`
	Stage         string    `json:"stage"`
	Kind          string    `json:"kind"`
	ModelID       string    `json:"modelId,omitempty"`
	UpstreamModel string    `json:"upstreamModel,omitempty"`
	ProviderID    string    `json:"providerId,omitempty"`
	TaskID        string    `json:"taskId,omitempty"`
	HTTPStatus    int       `json:"httpStatus,omitempty"`
	RequestID     string    `json:"requestId,omitempty"`
	Code          string    `json:"code,omitempty"`
	Message       string    `json:"message,omitempty"`
	Response      string    `json:"response,omitempty"`
}

var diagnosticURL = regexp.MustCompile(`(?i)(?:https?|s3|gs)://[^\s<>"']+`)
var diagnosticBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
var diagnosticSecret = regexp.MustCompile(`(?i)(api[_-]?key|access[_-]?key|secret|token|password|signature|authorization)(["'\s]*[:=]["'\s]*)([^\s,;"'}]+)`)

func diagnosticText(text string, secrets ...string) string {
	for _, secret := range secrets {
		if secret != "" {
			text = strings.ReplaceAll(text, secret, "[redacted]")
		}
	}
	text = diagnosticURL.ReplaceAllString(text, "[url redacted]")
	text = diagnosticBearer.ReplaceAllString(text, "Bearer [redacted]")
	text = diagnosticSecret.ReplaceAllString(text, "$1$2[redacted]")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return -1
		}
		return r
	}, text)
	if len(text) > 8192 {
		text = text[:8192]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return strings.TrimSpace(text)
}

func diagnosticJSON(raw []byte, secrets ...string) string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return diagnosticText(string(raw), secrets...)
	}
	var redact func(any) any
	redact = func(value any) any {
		switch v := value.(type) {
		case map[string]any:
			for key, item := range v {
				lower := strings.ToLower(key)
				if strings.Contains(lower, "key") || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "signature") || lower == "authorization" || lower == "prompt" || lower == "request" || lower == "input" || lower == "reference_images" || lower == "reference_videos" {
					v[key] = "[redacted]"
				} else {
					v[key] = redact(item)
				}
			}
		case []any:
			for i, item := range v {
				v[i] = redact(item)
			}
		case string:
			return diagnosticText(v, secrets...)
		}
		return value
	}
	out, _ := json.Marshal(redact(value))
	return diagnosticText(string(out), secrets...)
}

func providerDiagnosticFrom(err error) ProviderDiagnostic {
	var upstream *providerHTTPError
	if errors.As(err, &upstream) {
		return upstream.diagnostic
	}
	return ProviderDiagnostic{Message: diagnosticText(err.Error())}
}

func recordProviderDiagnostic(p *Project, index int, stage string, d ProviderDiagnostic) {
	d.At = time.Now().UTC()
	d.Shot = index
	d.Stage = stage
	if index >= 0 && index < len(p.Shots) {
		shot := p.Shots[index]
		d.Attempt = shot.Attempt
		d.TaskID = shot.VideoTaskID
		d.ProviderID = shot.ProviderID
		if strings.HasPrefix(stage, "image") || stage == "worker" && shot.Stage == "image" || p.Canvas != nil && p.Canvas.Kind == "image" {
			d.TaskID = shot.ImageTaskID
			d.Kind = "image"
		} else {
			d.Kind = "video"
		}
	}
	if d.Kind == "" {
		d.Kind = "video"
	}
	d.ModelID = p.Brief.VideoModel
	if d.Kind == "image" {
		d.ModelID = p.Brief.ImageModel
	}
	if p.Canvas != nil {
		d.ModelID = p.Canvas.ModelID
	}
	for _, model := range p.Models {
		if model.ID == d.ModelID {
			d.UpstreamModel = model.UpstreamModel
			if d.ProviderID == "" {
				d.ProviderID = model.ProviderID
			}
		}
	}
	d.Message = diagnosticText(d.Message)
	d.Response = diagnosticText(d.Response)
	d.RequestID = diagnosticText(d.RequestID)
	d.Code = diagnosticText(d.Code)
	if n := len(p.ProviderDiagnostics); n > 0 {
		last := p.ProviderDiagnostics[n-1]
		if last.Shot == d.Shot && last.Attempt == d.Attempt && last.Stage == d.Stage && last.HTTPStatus == d.HTTPStatus && last.Code == d.Code && last.Message == d.Message && last.Response == d.Response {
			return
		}
	}
	p.ProviderDiagnostics = append(slices.Clone(p.ProviderDiagnostics), d)
	if len(p.ProviderDiagnostics) > 64 {
		p.ProviderDiagnostics = slices.Clone(p.ProviderDiagnostics[len(p.ProviderDiagnostics)-64:])
	}
}
