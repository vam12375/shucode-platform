package media

import "testing"

func TestVideoSubmitBodyGenerateAudio(t *testing.T) {
	m := Model{UpstreamModel: "doubao-seedance-2-0-mini-260615", GenerateAudio: true}
	b := Brief{Resolution: "720p", AspectRatio: "16:9"}
	s := ShotPlan{Prompt: "商品展示", Duration: 5}

	body := videoSubmitBody(m, b, s, "https://example.test/frame.png")
	if body["generate_audio"] != true {
		t.Fatalf("generate_audio = %v, want true", body["generate_audio"])
	}
	if body["model"] != m.UpstreamModel || body["resolution"] != "720p" || body["duration"] != 5 {
		t.Fatalf("unexpected submit body: %#v", body)
	}

	m.GenerateAudio = false
	body = videoSubmitBody(m, b, s, "https://example.test/frame.png")
	if _, exists := body["generate_audio"]; exists {
		t.Fatal("generate_audio must be omitted when the model switch is off")
	}
}
