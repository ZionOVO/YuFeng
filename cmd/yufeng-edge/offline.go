package main

// decideOfflineStart 断网自治：世代与监听计划都有已验证缓存才继续服务。
func decideOfflineStart(generationCached, listenPlanCached bool, brainErr error) error {
	if brainErr == nil {
		return nil
	}
	if generationCached && listenPlanCached {
		return nil
	}
	return brainErr
}
