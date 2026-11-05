package media

// 编译器升级不能让已受理记录在恢复校验时变成另一份供应商输入；版本随执行段冻结。
func frozenReferenceScriptVersion(p *Project) int {
	if len(p.Shots) == 0 {
		return 1
	}
	version := p.Shots[0].ReferenceScriptVersion
	for _, shot := range p.Shots {
		if shot.ReferenceScriptVersion != version {
			return -1
		}
	}
	return version
}

func revisionReferenceScriptVersion(p *Project, plan Plan) int {
	if p.Plan != nil && equalAnalysisPlan(withoutReferenceClipBindings(*p.Plan), withoutReferenceClipBindings(plan)) {
		return frozenReferenceScriptVersion(p)
	}
	return 1
}
