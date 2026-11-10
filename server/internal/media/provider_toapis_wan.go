package media

// Wan 的音频、参考图与参考视频字段不同于 Seedance，不能只放开型号后复用原报文。
// 先按平台的首尾帧/多模态语义组装，再转换成 Wan 明确支持的字段；首尾帧保持原样。
func applyToAPIsWanParameters(body map[string]any, m Model) {
	if !toapisWanModel(m.UpstreamModel) {
		return
	}
	body["ratio"] = body["aspect_ratio"]
	delete(body, "aspect_ratio")
	body["audio"] = body["generate_audio"]
	delete(body, "generate_audio")
	body["watermark"] = false
	// 制作单已包含完整分镜，避免上游默认改写改变模板中的动作与事实。
	body["prompt_extend"] = false
	if images, ok := body["image_with_roles"].([]map[string]string); ok && len(images) > 0 && images[0]["role"] == "reference_image" {
		urls := make([]string, 0, len(images))
		for _, image := range images {
			urls = append(urls, image["url"])
		}
		body["reference_images"] = urls
		delete(body, "image_with_roles")
	}
	if videos, ok := body["video_with_roles"].([]map[string]string); ok {
		items := make([]map[string]string, 0, len(videos))
		for _, video := range videos {
			items = append(items, map[string]string{"video_url": video["url"]})
		}
		body["video_list"] = items
		delete(body, "video_with_roles")
	}
}
