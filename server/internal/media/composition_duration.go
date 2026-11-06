package media

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"time"
)

// 编码和封装会有不足0.1秒的尾部差异；不能按假定帧率向下取整后拒绝正常媒体。
const compositionDurationTolerance = .1

func finiteDuration(value string) (float64, bool) {
	seconds, err := strconv.ParseFloat(value, 64)
	return seconds, err == nil && !math.IsNaN(seconds) && !math.IsInf(seconds, 0) && seconds > 0 && seconds <= 600
}

func durationMatches(actual, expected float64) bool {
	return expected > 0 && !math.IsNaN(actual) && !math.IsInf(actual, 0) && math.Abs(actual-expected) <= compositionDurationTolerance+1e-6
}

// 视频/音频分别探测；音轨或容器长度不能证明视频内容完整。无 duration 的流按包时间戳确定展示跨度。
func probeCompositionStreamSeconds(ctx context.Context, path, selector string) (float64, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-select_streams", selector, "-show_entries", "stream=duration", "-of", "json", path).Output()
	var metadata struct {
		Streams []struct {
			Duration string `json:"duration"`
		} `json:"streams"`
	}
	if err != nil || json.Unmarshal(raw, &metadata) != nil || len(metadata.Streams) != 1 {
		return 0, fmt.Errorf("composition_source_invalid")
	}
	if seconds, ok := finiteDuration(metadata.Streams[0].Duration); ok {
		return seconds, nil
	}
	raw, err = exec.CommandContext(ctx, "ffprobe", "-v", "error", "-protocol_whitelist", "file,pipe", "-select_streams", selector, "-show_packets", "-show_entries", "packet=pts_time,duration_time", "-of", "json", path).Output()
	var packets struct {
		Packets []struct {
			PTS      string `json:"pts_time"`
			Duration string `json:"duration_time"`
		} `json:"packets"`
	}
	if err != nil || json.Unmarshal(raw, &packets) != nil {
		return 0, fmt.Errorf("composition_source_invalid")
	}
	start, end := math.Inf(1), math.Inf(-1)
	for _, packet := range packets.Packets {
		pts, err := strconv.ParseFloat(packet.PTS, 64)
		duration, ok := finiteDuration(packet.Duration)
		if err != nil || math.IsNaN(pts) || math.IsInf(pts, 0) || !ok {
			return 0, fmt.Errorf("composition_source_invalid")
		}
		start = math.Min(start, pts)
		end = math.Max(end, pts+duration)
	}
	seconds := end - start
	if math.IsInf(seconds, 0) || math.IsNaN(seconds) || seconds <= 0 || seconds > 600 {
		return 0, fmt.Errorf("composition_source_invalid")
	}
	return seconds, nil
}

func compositionSourceCovers(actual, end float64) bool {
	return actual > 0 && !math.IsNaN(actual) && !math.IsInf(actual, 0) && end > 0 && end <= actual+compositionDurationTolerance+1e-6
}
