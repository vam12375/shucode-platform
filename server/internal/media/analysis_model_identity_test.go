package media

import "testing"

func TestDeepSeekAnalysisIdentityRequiresRealFlashFamily(t *testing.T) {
	for _, model := range []string{"deepseek-flash", "deepseek-v4-flash", "DeepSeek-V4.1-Flash", "deepseek_v4_1_flash", "deepseek-v4.1-flash-20261023"} {
		if !IsDeepSeekAnalysisModel(model) {
			t.Errorf("real Flash family was rejected: %s", model)
		}
	}
	for _, model := range []string{"deepseek", "deepseek-pro", "deepseek-v4-pro", "deepseek-notflash", "deepseek-v4-flash-pro", "alias-deepseek-flash", "other-flash", "deepseekanything-flash"} {
		if IsDeepSeekAnalysisModel(model) {
			t.Errorf("non Flash identity was accepted: %s", model)
		}
	}
}
